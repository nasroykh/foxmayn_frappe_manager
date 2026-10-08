package bench

// DevServerStartCmd starts a dev bench's honcho stack (`bench start`) in the
// background inside the frappe container.
const DevServerStartCmd = "cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"

// devServerStopCmd stops the running honcho stack and waits up to ten seconds
// for it to exit, so the new one does not race the old for its ports.
//
// The pattern is "[h]oncho start", not "honcho start": pkill -f matches
// against full command lines, and the `bash -c` running this script has the
// pattern in its own command line. With the plain pattern pkill killed that
// shell before it reached `bench start`, so every dev restart through this path
// (domain changes, reconcile, set-proxy, tunnel) left the bench with no web
// server, workers or scheduler.
const devServerStopCmd = "pkill -f '[h]oncho start' 2>/dev/null;" +
	" for i in $(seq 1 20); do pgrep -f '[h]oncho start' >/dev/null || break; sleep 0.5; done"

// DevServerRestartCmd stops a dev bench's honcho stack and starts it again.
const DevServerRestartCmd = devServerStopCmd + "; " + DevServerStartCmd
