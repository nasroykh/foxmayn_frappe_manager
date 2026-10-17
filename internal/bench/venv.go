package bench

// RepairVenvCmd re-points a bench's virtualenv at the image's interpreter when
// the one it was made with is gone.
//
// A venv's bin/python is an absolute link to a patch release
// (/home/frappe/.pyenv/versions/3.14.7/bin/python3), and the venv lives in
// ./workspace while the interpreter lives in the image. An image built from a
// newer base ships another patch release, the link dangles, and every bench
// command dies in os.execv with FileNotFoundError. Patch releases of one minor
// share the ABI and the lib/python3.X directory, so pointing the link at the
// newest installed patch of the same minor is a complete repair. A different
// minor is not: the installed packages would not import.
//
// It prints nothing when the link resolves or bin/python is not a link,
// "FFM_VENV=relinked <old> <new>" after a repair, and exits 3 with
// "FFM_VENV=gone <version>" or "FFM_VENV=unknown <target>" when it cannot
// repair.
var RepairVenvCmd = repairVenvScript("/workspace/frappe-bench/env", "/home/frappe/.pyenv/versions/")

// repairVenvScript builds RepairVenvCmd for an env directory and a pyenv
// versions directory (with its trailing slash); tests run it on temp dirs.
func repairVenvScript(envDir, versionsDir string) string {
	return `e=` + ShellQuote(envDir) + `; p=` + ShellQuote(versionsDir) + `
t=$(readlink "$e/bin/python") || exit 0
[ -e "$e/bin/python" ] && exit 0
case $t in "$p"*) ;; *) echo "FFM_VENV=unknown $t"; exit 3;; esac
v=${t#"$p"}; v=${v%%/*}
n=$(ls -d "$p${v%.*}".[0-9]* 2>/dev/null | sort -V | tail -n1); n=${n#"$p"}
nt=$p$n${t#"$p$v"}
if [ -z "$n" ] || [ ! -e "$nt" ]; then echo "FFM_VENV=gone $v"; exit 3; fi
ln -sfn "$nt" "$e/bin/python"
r=${v//./\\.}
sed -i -E "s#/versions/$r(/|\$)#/versions/$n\1#; s#^(version(_info)?) = $r\$#\1 = $n#" "$e/pyvenv.cfg"
echo "FFM_VENV=relinked $v $n"`
}
