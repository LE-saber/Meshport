//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const createNoWindow = 0x08000000

func osName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer k.Close()
	name, _, _ := k.GetStringValue("ProductName")
	build, _, _ := k.GetStringValue("CurrentBuild")
	disp, _, _ := k.GetStringValue("DisplayVersion")
	if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	if name == "" {
		name = "Windows"
	}
	if disp != "" {
		name += " " + disp
	}
	return name
}

type powerStatus struct {
	ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag byte
	BatteryLifeTime, BatteryFullLifeTime                            uint32
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

func deviceKind() string {
	var st powerStatus
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r != 0 && st.BatteryFlag != 128 && st.BatteryFlag != 255 {
		return "laptop"
	}
	return "desktop"
}

// The release build uses -H windowsgui, so there is no console.
func isTerminal() bool { return false }

func logFatal(err error) {
	t, _ := windows.UTF16PtrFromString(err.Error())
	c, _ := windows.UTF16PtrFromString("Meshport")
	_, _ = windows.MessageBox(0, t, c, windows.MB_ICONERROR|windows.MB_OK)
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

func browserCandidates() []string {
	pf, pf86, local := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")
	return []string{
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(local, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
	}
}

// openWindow opens the UI in an Edge/Chrome --app window (Edge ships with
// Windows 10/11), falling back to the default browser.
func openWindow(url, profile string) error {
	for _, p := range browserCandidates() {
		if _, err := os.Stat(p); err == nil {
			return exec.Command(p, "--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--window-size=1100,760").Start()
		}
	}
	return hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", url)).Start()
}

func openPath(p string) error {
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return exec.Command("explorer.exe", p).Start()
	}
	return hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", p)).Start()
}

var (
	notifyMu   sync.Mutex
	notifyLast time.Time
)

// notify shows a toast through PowerShell's registered AppUserModelID. The
// text is passed in environment variables, never interpolated into the script.
const toastScript = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
$x = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$t = $x.GetElementsByTagName('text')
$t.Item(0).AppendChild($x.CreateTextNode($env:MP_TITLE)) | Out-Null
$t.Item(1).AppendChild($x.CreateTextNode($env:MP_BODY)) | Out-Null
$n = [Windows.UI.Notifications.ToastNotification]::new($x)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($n)`

func notify(title, body string) {
	notifyMu.Lock()
	if time.Since(notifyLast) < 1500*time.Millisecond {
		notifyMu.Unlock()
		return
	}
	notifyLast = time.Now()
	notifyMu.Unlock()
	cmd := hidden(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", toastScript))
	cmd.Env = append(os.Environ(), "MP_TITLE="+title, "MP_BODY="+body)
	_ = cmd.Run()
}

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue("Meshport"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("Meshport", fmt.Sprintf(`"%s" --minimized`, exe))
}
