package manager

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// What a Frappe database dump says about the site it came from. Frappe's backup
// does not list the apps, so the only place to learn them is the dump's own
// `tabInstalled Application` table.
type dumpApp struct {
	Name    string
	Version string
	// Branch is the app's git_branch column, "" when absent.
	Branch string
}

type dumpInfo struct {
	// Engine is "mariadb" or "postgres"; empty when the dump says neither.
	Engine string
	// HasAuth and HasUser report the __Auth and tabUser tables. A dump without
	// them restores into a site nobody can log in to.
	HasAuth, HasUser bool
	// HasApps reports that the installed-applications table was found.
	HasApps bool
	Apps    []dumpApp
	// MetaVersion and MetaBranch come from the "-- begin frappe metadata"
	// comment block recent Frappe versions put at the top of the dump.
	MetaVersion, MetaBranch string
}

// FrappeVersion returns the frappe app's version string, or "".
func (d dumpInfo) FrappeVersion() string {
	for _, a := range d.Apps {
		if a.Name == "frappe" && a.Version != "" {
			return a.Version
		}
	}
	return d.MetaVersion
}

// FrappeBranch returns the branch the frappe app was on, or "".
func (d dumpInfo) FrappeBranch() string {
	for _, a := range d.Apps {
		if a.Name == "frappe" && a.Branch != "" {
			return a.Branch
		}
	}
	return d.MetaBranch
}

// isTextSQL reports whether b looks like a text dump rather than binary data.
// GPG-encrypted backups (Frappe's encrypt_backup) are binary packets.
func isTextSQL(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c == 0 || (c < 0x20 && c != '\n' && c != '\r' && c != '\t') {
			return false
		}
	}
	return true
}

const errEncryptedDump = "the database dump is not a gzip or SQL file. If it is a GPG-encrypted Frappe backup " +
	"(System Settings > Encrypt backups), decrypt it first with 'gpg --decrypt' and the backup_encryption_key from " +
	"its site_config.json: ffm does not import encrypted Frappe backups"

// openDump opens a Frappe database dump and returns a reader of its SQL text.
func openDump(path string) (io.Reader, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	head, _ := br.Peek(512)
	closeFn := func() { f.Close() }
	if len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			closeFn()
			return nil, nil, fmt.Errorf("the database dump is not valid gzip: %w", err)
		}
		inner := bufio.NewReaderSize(zr, 1<<20)
		ih, _ := inner.Peek(512)
		if !isTextSQL(ih) {
			closeFn()
			return nil, nil, errors.New(errEncryptedDump)
		}
		return inner, closeFn, nil
	}
	if !isTextSQL(head) {
		closeFn()
		return nil, nil, errors.New(errEncryptedDump)
	}
	return br, closeFn, nil
}

// maxKeptLine bounds how much of one SQL line is kept in memory. Only the
// installed-applications table's rows are kept at all; every other line (a
// single INSERT of a big table can be hundreds of megabytes) is read past.
const maxKeptLine = 8 << 20

const appsTable = "tabInstalled Application"

// inspectDump streams the whole dump once. It reads to the end on purpose: the
// gzip trailer's checksum is only verified there, so a truncated download is
// caught here instead of halfway through `bench restore`.
func inspectDump(r io.Reader) (dumpInfo, error) {
	var d dumpScanner
	br := bufio.NewReaderSize(r, 1<<20)
	var cur []byte
	atStart, keep := true, false
	for {
		chunk, err := br.ReadSlice('\n')
		if err != nil && err != bufio.ErrBufferFull && err != io.EOF {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return dumpInfo{}, errors.New("the database dump is truncated (unexpected end of the gzip stream)")
			}
			return dumpInfo{}, fmt.Errorf("read the database dump: %w", err)
		}
		if len(chunk) > 0 {
			if atStart {
				keep = interestingLine(chunk)
				cur = cur[:0]
			}
			if keep {
				if len(cur) < maxKeptLine {
					cur = append(cur, chunk...)
				}
			} else if atStart {
				if lerr := d.line(chunk); lerr != nil {
					return dumpInfo{}, lerr
				}
			}
		}
		atStart = err != bufio.ErrBufferFull
		if atStart && keep && len(chunk) > 0 {
			if lerr := d.line(cur); lerr != nil {
				return dumpInfo{}, lerr
			}
			keep = false
		}
		if err == io.EOF {
			break
		}
	}
	return d.info, nil
}

// interestingLine marks the lines whose full text is needed: the data of the
// installed-applications table.
func interestingLine(b []byte) bool {
	if !(bytes.HasPrefix(b, []byte("INSERT INTO")) || bytes.HasPrefix(b, []byte("COPY "))) {
		return false
	}
	head := b
	if len(head) > 160 {
		head = head[:160]
	}
	return bytes.Contains(head, []byte(appsTable))
}

// dumpScanner is a small line state machine over a mysqldump or pg_dump file.
type dumpScanner struct {
	info      dumpInfo
	inCreate  bool
	createCol []string
	inCopy    bool
	copyCols  []string
	// insertBuf accumulates an INSERT whose VALUES list continues on the next
	// lines, which is how mysqldump and mariadb-dump write it.
	insertBuf  string
	inInsert   bool
	inMetadata bool
}

func (d *dumpScanner) line(raw []byte) error {
	b := strings.TrimRight(string(raw), "\r\n")
	if d.info.Engine == "" {
		switch {
		case strings.HasPrefix(b, "-- MariaDB dump"), strings.HasPrefix(b, "-- MySQL dump"):
			d.info.Engine = "mariadb"
		case strings.HasPrefix(b, "-- PostgreSQL database dump"):
			d.info.Engine = "postgres"
		case strings.HasPrefix(b, "COPY "):
			d.info.Engine = "postgres"
		case strings.HasPrefix(b, "CREATE TABLE `"), strings.HasPrefix(b, "INSERT INTO `"):
			d.info.Engine = "mariadb"
		}
	}
	if d.inMetadata {
		switch {
		case strings.HasPrefix(b, "-- end frappe metadata"):
			d.inMetadata = false
		case strings.HasPrefix(b, "-- version = "):
			d.info.MetaVersion = strings.TrimSpace(strings.TrimPrefix(b, "-- version = "))
		case strings.HasPrefix(b, "-- branch = "):
			d.info.MetaBranch = strings.TrimSpace(strings.TrimPrefix(b, "-- branch = "))
		}
		return nil
	}
	if strings.HasPrefix(b, "-- begin frappe metadata") {
		d.inMetadata = true
		return nil
	}
	if d.inInsert {
		d.insertBuf += "\n" + b
		if len(d.insertBuf) > maxKeptLine {
			return errors.New("the installed-apps INSERT in this dump is implausibly large")
		}
		if strings.HasSuffix(strings.TrimSpace(b), ";") {
			d.inInsert = false
			buf := d.insertBuf
			d.insertBuf = ""
			return d.addInsert(buf)
		}
		return nil
	}
	if d.inCopy {
		if b == `\.` {
			d.inCopy = false
			return nil
		}
		d.addCopyRow(b)
		return nil
	}
	if d.inCreate {
		t := strings.TrimSpace(b)
		switch {
		case strings.HasPrefix(t, ")"):
			d.inCreate = false
		case strings.HasPrefix(t, "`"):
			if end := strings.Index(t[1:], "`"); end > 0 {
				d.createCol = append(d.createCol, t[1:1+end])
			}
		}
		return nil
	}

	switch {
	case strings.HasPrefix(b, "CREATE TABLE"):
		switch {
		case hasTableName(b, "__Auth"):
			d.info.HasAuth = true
		case hasTableName(b, "tabUser"):
			d.info.HasUser = true
		case hasTableName(b, appsTable):
			d.info.HasApps = true
			d.inCreate, d.createCol = true, nil
		}
	case strings.HasPrefix(b, "COPY ") && strings.Contains(b[:min(len(b), 160)], appsTable):
		open := strings.Index(b, "(")
		closeP := strings.Index(b, ")")
		if open < 0 || closeP < open {
			return errors.New("cannot read the installed-apps table of this dump (COPY without a column list)")
		}
		d.info.HasApps = true
		d.copyCols = splitColumns(b[open+1 : closeP])
		d.inCopy = true
	case strings.HasPrefix(b, "INSERT INTO") && strings.Contains(b[:min(len(b), 160)], appsTable):
		d.info.HasApps = true
		if !strings.HasSuffix(strings.TrimSpace(b), ";") {
			d.inInsert, d.insertBuf = true, b
			return nil
		}
		return d.addInsert(b)
	}
	return nil
}

// hasTableName reports whether a CREATE TABLE line names exactly this table,
// quoted the MariaDB way (`x`) or the Postgres way ("x").
func hasTableName(line, table string) bool {
	return strings.Contains(line, "`"+table+"`") || strings.Contains(line, `"`+table+`"`)
}

func splitColumns(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		c = strings.Trim(strings.TrimSpace(c), "`\"")
		out = append(out, c)
	}
	return out
}

func colIndex(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

func (d *dumpScanner) addCopyRow(row string) {
	fields := strings.Split(row, "\t")
	ni, vi, bi := colIndex(d.copyCols, "app_name"), colIndex(d.copyCols, "app_version"), colIndex(d.copyCols, "git_branch")
	if ni < 0 || ni >= len(fields) || fields[ni] == `\N` {
		return
	}
	app := dumpApp{Name: fields[ni]}
	if vi >= 0 && vi < len(fields) && fields[vi] != `\N` {
		app.Version = fields[vi]
	}
	if bi >= 0 && bi < len(fields) && fields[bi] != `\N` {
		app.Branch = fields[bi]
	}
	d.info.Apps = append(d.info.Apps, app)
}

// addInsert reads `INSERT INTO <table> [(cols)] VALUES (...),(...);`.
func (d *dumpScanner) addInsert(line string) error {
	i := strings.Index(line, appsTable)
	rest := line[i+len(appsTable):]
	if rest != "" && (rest[0] == '`' || rest[0] == '"') {
		rest = rest[1:]
	}
	rest = strings.TrimSpace(rest)
	cols := d.createCol
	if strings.HasPrefix(rest, "(") {
		end := strings.Index(rest, ")")
		if end < 0 {
			return errors.New("cannot read the installed-apps rows of this dump")
		}
		cols = splitColumns(rest[1:end])
		rest = strings.TrimSpace(rest[end+1:])
	}
	if !strings.HasPrefix(strings.ToUpper(rest), "VALUES") {
		return errors.New("cannot read the installed-apps rows of this dump (no VALUES)")
	}
	rest = strings.TrimSpace(rest[len("VALUES"):])
	rows, err := parseSQLTuples(rest)
	if err != nil {
		return fmt.Errorf("cannot read the installed-apps rows of this dump: %w", err)
	}
	ni, vi, bi := colIndex(cols, "app_name"), colIndex(cols, "app_version"), colIndex(cols, "git_branch")
	if ni < 0 {
		return errors.New("cannot tell which column of the installed-apps table holds the app name " +
			"(the dump has neither a column list nor a readable CREATE TABLE)")
	}
	for _, r := range rows {
		if ni >= len(r) {
			return errors.New("an installed-apps row is shorter than its table definition")
		}
		app := dumpApp{Name: r[ni]}
		if vi >= 0 && vi < len(r) {
			app.Version = r[vi]
		}
		if bi >= 0 && bi < len(r) {
			app.Branch = r[bi]
		}
		d.info.Apps = append(d.info.Apps, app)
	}
	return nil
}

// parseSQLTuples parses "(a,'b',NULL),(...);" into rows of unquoted values. NULL
// comes back as the empty string. It handles the MySQL string escapes and the
// doubled quote; nothing else is needed for the tables ffm reads.
func parseSQLTuples(s string) ([][]string, error) {
	var rows [][]string
	i := 0
	skipWS := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
			i++
		}
	}
	for {
		skipWS()
		if i >= len(s) || s[i] == ';' {
			return rows, nil
		}
		if s[i] == ',' {
			i++
			continue
		}
		if s[i] != '(' {
			return nil, fmt.Errorf("unexpected %q in a VALUES list", s[i])
		}
		i++
		var row []string
		for {
			skipWS()
			if i >= len(s) {
				return nil, errors.New("a VALUES list ends inside a row")
			}
			var val strings.Builder
			if s[i] == '\'' {
				i++
				for {
					if i >= len(s) {
						return nil, errors.New("a string is not terminated")
					}
					c := s[i]
					switch {
					case c == '\\' && i+1 < len(s):
						i++
						switch s[i] {
						case 'n':
							val.WriteByte('\n')
						case 't':
							val.WriteByte('\t')
						case 'r':
							val.WriteByte('\r')
						case '0':
							val.WriteByte(0)
						default:
							val.WriteByte(s[i])
						}
						i++
						continue
					case c == '\'' && i+1 < len(s) && s[i+1] == '\'':
						val.WriteByte('\'')
						i += 2
						continue
					case c == '\'':
						i++
					default:
						val.WriteByte(c)
						i++
						continue
					}
					break
				}
			} else {
				start := i
				for i < len(s) && s[i] != ',' && s[i] != ')' {
					i++
				}
				tok := strings.TrimSpace(s[start:i])
				if !strings.EqualFold(tok, "NULL") {
					val.WriteString(tok)
				}
			}
			row = append(row, val.String())
			skipWS()
			if i >= len(s) {
				return nil, errors.New("a VALUES list ends inside a row")
			}
			if s[i] == ',' {
				i++
				continue
			}
			if s[i] == ')' {
				i++
				break
			}
			return nil, fmt.Errorf("unexpected %q in a row", s[i])
		}
		rows = append(rows, row)
	}
}
