package manager

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mariaHeader = `-- MariaDB dump 10.19  Distrib 10.11.6-MariaDB, for debian-linux-gnu (x86_64)
--
-- Host: mariadb    Database: _1bd3e0294da19198
-- ------------------------------------------------------
/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
`

const mariaAuth = "DROP TABLE IF EXISTS `__Auth`;\nCREATE TABLE `__Auth` (\n  `doctype` varchar(140) NOT NULL,\n  `name` varchar(255) NOT NULL,\n  `fieldname` varchar(140) NOT NULL,\n  `password` text NOT NULL,\n  `encrypted` int(1) NOT NULL DEFAULT 0,\n  PRIMARY KEY (`doctype`,`name`,`fieldname`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n"

const mariaAppsCreate = "CREATE TABLE `tabInstalled Application` (\n  `name` varchar(140) NOT NULL,\n  `creation` datetime(6) DEFAULT NULL,\n  `modified` datetime(6) DEFAULT NULL,\n  `modified_by` varchar(255) DEFAULT NULL,\n  `owner` varchar(255) DEFAULT NULL,\n  `docstatus` int(1) NOT NULL DEFAULT 0,\n  `idx` int(8) NOT NULL DEFAULT 0,\n  `app_name` varchar(140) DEFAULT NULL,\n  `app_version` varchar(140) DEFAULT NULL,\n  `git_branch` varchar(140) DEFAULT NULL,\n  `parent` varchar(140) DEFAULT NULL,\n  `parentfield` varchar(140) DEFAULT NULL,\n  `parenttype` varchar(140) DEFAULT NULL,\n  PRIMARY KEY (`name`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n"

const mariaUser = "CREATE TABLE `tabUser` (\n  `name` varchar(140) NOT NULL,\n  PRIMARY KEY (`name`)\n) ENGINE=InnoDB;\nCREATE TABLE `tabUser Permission` (\n  `name` varchar(140) NOT NULL\n) ENGINE=InnoDB;\n"

func appRow(i int, app, ver string) string {
	return fmt.Sprintf("('%s%d','2024-01-01 00:00:00.000000','2024-01-01 00:00:00.000000','Administrator','Administrator',0,%d,'%s','%s','version-15','DefaultValue','installed_applications','Installed Applications')",
		app, i, i, app, ver)
}

func mariaDump(apps ...string) string {
	var rows []string
	for i, a := range apps {
		name, ver, _ := strings.Cut(a, "@")
		rows = append(rows, appRow(i+1, name, ver))
	}
	return mariaHeader + mariaAuth + mariaAppsCreate +
		"/*!40000 ALTER TABLE `tabInstalled Application` DISABLE KEYS */;\nINSERT INTO `tabInstalled Application` VALUES " +
		strings.Join(rows, ",") + ";\n" + mariaUser
}

func TestInspectDumpMariaDB(t *testing.T) {
	info, err := inspectDump(strings.NewReader(mariaDump("frappe@15.38.0", "erpnext@15.40.1", "my_app@0.0.1")))
	if err != nil {
		t.Fatal(err)
	}
	if info.Engine != "mariadb" || !info.HasAuth || !info.HasUser || !info.HasApps {
		t.Errorf("info = %+v", info)
	}
	if len(info.Apps) != 3 || info.Apps[1].Name != "erpnext" || info.Apps[1].Version != "15.40.1" || info.FrappeVersion() != "15.38.0" {
		t.Errorf("apps = %+v", info.Apps)
	}
}

func TestInspectDumpExplicitColumnsAndSkipExtendedInsert(t *testing.T) {
	d := mariaHeader + mariaAuth + mariaUser +
		"INSERT INTO `tabInstalled Application` (`app_version`,`app_name`) VALUES ('15.1.0','frappe');\n" +
		"INSERT INTO `tabInstalled Application` (`app_version`,`app_name`) VALUES ('1.0','it''s_x'),('2.0',NULL);\n"
	info, err := inspectDump(strings.NewReader(d))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Apps) != 3 || info.Apps[0].Name != "frappe" || info.Apps[1].Name != "it's_x" || info.Apps[2].Name != "" {
		t.Errorf("apps = %+v", info.Apps)
	}
}

func TestInspectDumpStringEscapes(t *testing.T) {
	rows, err := parseSQLTuples(`('a\'b','c,d)e',NULL,12,0x41),('x\\y','',-1.5,'z');`)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a'b", "c,d)e", "", "12", "0x41"}, {`x\y`, "", "-1.5", "z"}}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
	for _, bad := range []string{`('a`, `(1,2`, `x`} {
		if _, err := parseSQLTuples(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestInspectDumpPostgres(t *testing.T) {
	d := "--\n-- PostgreSQL database dump\n--\nSET statement_timeout = 0;\n" +
		"CREATE TABLE public.\"__Auth\" (\n    doctype character varying(140)\n);\n" +
		"CREATE TABLE public.\"tabUser\" (\n    name character varying(140)\n);\n" +
		"COPY public.\"tabInstalled Application\" (name, creation, idx, app_name, app_version, git_branch) FROM stdin;\n" +
		"a1\t2024-01-01\t1\tfrappe\t16.0.1\tversion-16\n" +
		"a2\t2024-01-01\t2\terpnext\t16.2.0\t\\N\n" +
		"\\.\n" +
		"COPY public.\"tabUser\" (name) FROM stdin;\nAdministrator\n\\.\n"
	info, err := inspectDump(strings.NewReader(d))
	if err != nil {
		t.Fatal(err)
	}
	if info.Engine != "postgres" || !info.HasAuth || !info.HasUser || len(info.Apps) != 2 || info.FrappeVersion() != "16.0.1" {
		t.Errorf("info = %+v", info)
	}
}

func TestInspectDumpMissingTables(t *testing.T) {
	info, err := inspectDump(strings.NewReader(mariaHeader + mariaAppsCreate))
	if err != nil {
		t.Fatal(err)
	}
	if info.HasAuth || info.HasUser {
		t.Errorf("a dump without __Auth / tabUser reports them: %+v", info)
	}
	// `tabUser Permission` is not `tabUser`.
	info, _ = inspectDump(strings.NewReader("CREATE TABLE `tabUser Permission` (\n  `name` varchar(1)\n);\n"))
	if info.HasUser {
		t.Error("tabUser Permission counted as tabUser")
	}
}

func TestInspectDumpSkipsHugeLinesAndStillFindsLaterTables(t *testing.T) {
	var b strings.Builder
	b.WriteString(mariaHeader + mariaAuth + mariaAppsCreate)
	// A 24 MiB single INSERT of some other table, bigger than the read buffer
	// and the kept-line cap.
	b.WriteString("INSERT INTO `tabVersion` VALUES ('" + strings.Repeat("x", 24<<20) + "');\n")
	b.WriteString("INSERT INTO `tabInstalled Application` VALUES " + appRow(1, "frappe", "15.0.0") + ";\n")
	b.WriteString(mariaUser)
	info, err := inspectDump(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasUser || len(info.Apps) != 1 {
		t.Errorf("info = %+v", info)
	}
}

func TestInspectDumpAppRowLongerThanTheReadBuffer(t *testing.T) {
	// 3000 apps: one INSERT line of well over 1 MiB, read in several chunks.
	var rows []string
	for i := 0; i < 3000; i++ {
		rows = append(rows, appRow(i, fmt.Sprintf("app_%d", i), "1.0")+strings.Repeat(" ", 400))
	}
	d := mariaHeader + mariaAuth + mariaAppsCreate + mariaUser +
		"INSERT INTO `tabInstalled Application` VALUES " + strings.Join(rows, ",") + ";\n"
	info, err := inspectDump(strings.NewReader(d))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Apps) != 3000 || info.Apps[2999].Name != "app_2999" {
		t.Errorf("apps = %d", len(info.Apps))
	}
}

func TestOpenDump(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		return p
	}

	r, closeFn, err := openDump(write("a.sql.gz", gzipBytes(mariaDump("frappe@15.0.0"))))
	if err != nil {
		t.Fatal(err)
	}
	info, err := inspectDump(r)
	closeFn()
	if err != nil || len(info.Apps) != 1 {
		t.Errorf("gz: %+v %v", info, err)
	}

	r, closeFn, err = openDump(write("b.sql", []byte(mariaDump("frappe@15.0.0"))))
	if err != nil {
		t.Fatal(err)
	}
	info, _ = inspectDump(r)
	closeFn()
	if len(info.Apps) != 1 {
		t.Errorf("plain: %+v", info)
	}

	// GPG-encrypted (Frappe's encrypt_backup): binary, not gzip.
	gpg := append([]byte{0x8c, 0x0d, 0x04, 0x09, 0x03, 0x02}, bytes.Repeat([]byte{0xde, 0xad, 0x00, 0xbe}, 100)...)
	if _, _, err := openDump(write("c.sql.gz", gpg)); err == nil || !strings.Contains(err.Error(), "GPG-encrypted") {
		t.Errorf("gpg: %v", err)
	}
	// Garbage under a .gz name.
	if _, _, err := openDump(write("d.sql.gz", []byte{0x1f, 0x8b, 0x00, 0x00})); err == nil {
		t.Error("a broken gzip header was accepted")
	}
	// Encrypted content inside a valid gzip.
	if _, _, err := openDump(write("e.sql.gz", gzipBytes(string(gpg)))); err == nil {
		t.Error("binary inside gzip accepted")
	}
}

func TestInspectDumpTruncatedGzip(t *testing.T) {
	full := gzipBytes(mariaDump("frappe@15.0.0") + strings.Repeat("-- filler line\n", 20000))
	p := filepath.Join(t.TempDir(), "t.sql.gz")
	os.WriteFile(p, full[:len(full)/2], 0o600)
	r, closeFn, err := openDump(p)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, err := inspectDump(r); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Errorf("err = %v", err)
	}
}
