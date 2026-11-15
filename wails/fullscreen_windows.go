package main

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	gwlStyleIndex           = ^uintptr(15)
	wsCaption               = 0x00C00000
	wsExTopmost             = 0x00000008
	monitorDefaultToNearest = 0x00000002
	dwmwaCloaked            = 14
	swpNoSize               = 0x0001
	swpNoMove               = 0x0002
	fullscreenPollInterval  = 500 * time.Millisecond
	fullscreenRestoreDelay  = 1200 * time.Millisecond
	// 连续两次采样都判定为全屏才隐藏（约 1 秒）：单次采样容易被最大化窗口、
	// 通知弹窗、切换动画误触发，表现为桌宠一闪一闪（issue #2）。
	fullscreenConfirmTicks = 2
)

type monitorInfo struct {
	CbSize    uint32
	RcMonitor RECT
	RcWork    RECT
	DwFlags   uint32
}

var (
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procSHQueryUserNotificationState = shell32.NewProc("SHQueryUserNotificationState")
	procGetForegroundWindow          = user32.NewProc("GetForegroundWindow")
	procGetWindowRect                = user32.NewProc("GetWindowRect")
	procGetClassNameW                = user32.NewProc("GetClassNameW")
	procIsIconic                     = user32.NewProc("IsIconic")
	procIsZoomed                     = user32.NewProc("IsZoomed")
	procMonitorFromWindow            = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW              = user32.NewProc("GetMonitorInfoW")
	procDwmGetWindowAttribute        = dwmapi.NewProc("DwmGetWindowAttribute")
)

// 桌面、任务栏、开始菜单等铺满屏幕但不是“全屏应用”的窗口
var shellWindowClasses = map[string]bool{
	"Progman":                    true,
	"WorkerW":                    true,
	"Shell_TrayWnd":              true,
	"Shell_SecondaryTrayWnd":     true,
	"Windows.UI.Core.CoreWindow": true,
	"MultitaskingViewFrame":      true,
	"ForegroundStaging":          true,
}

func (a *App) fullscreenEnabled() bool {
	return a.getBool("fullscreenHide", true)
}

func (a *App) fullscreenLoop() {
	t := time.NewTicker(fullscreenPollInterval)
	defer t.Stop()
	for range t.C {
		a.pollFullscreen()
	}
}

func (a *App) pollFullscreen() {
	a.mu.Lock()
	ready := !a.quitting && a.hwnd != 0 && a.petInit
	a.mu.Unlock()
	if !ready {
		return
	}

	enabled := a.fullscreenEnabled()
	active := enabled && immersiveFullscreenActive()
	now := time.Now()

	a.mu.Lock()
	if a.quitting || a.hwnd == 0 {
		a.mu.Unlock()
		return
	}
	h := a.hwnd
	if !enabled {
		a.fsHits = 0
		a.fsClearSince = time.Time{}
		// 关掉避让时如果正处于「被全屏藏起来」的状态，要立刻把窗口放回来，
		// 否则设置里关掉开关后桌宠会一直不出现
		wasFsHidden := a.fsHidden
		a.fsHidden = false
		hidden := a.hidden
		a.mu.Unlock()
		if wasFsHidden && !hidden {
			a.restoreAfterFullscreen(h)
			return
		}
		if !hidden {
			ensureTopmost(h)
		}
		return
	}

	// 面板打开 / 拖拽中：用户正在操作，任何全屏判定都先放一边，避免面板被一起藏掉
	if a.panelOpen || a.dragging {
		a.fsHits = 0
		a.mu.Unlock()
		return
	}

	if active {
		a.fsHits++
		a.fsClearSince = time.Time{}
		// 需要连续命中，单次抖动不触发隐藏
		if a.fsHits < fullscreenConfirmTicks {
			a.mu.Unlock()
			return
		}
		if !a.hidden && !a.fsHidden {
			a.fsHidden = true
			a.mu.Unlock()
			procShowWindow.Call(h, swHide)
			return
		}
		a.mu.Unlock()
		return
	}

	a.fsHits = 0
	if a.fsHidden {
		// 退出全屏后要稳定一段时间才回来，否则全屏应用切换时会来回闪
		if a.fsClearSince.IsZero() {
			a.fsClearSince = now
		}
		if now.Sub(a.fsClearSince) < fullscreenRestoreDelay {
			a.mu.Unlock()
			return
		}
		a.fsHidden = false
		a.fsClearSince = time.Time{}
		show := !a.hidden
		a.mu.Unlock()
		if show {
			a.restoreAfterFullscreen(h)
		}
		return
	}
	a.fsClearSince = time.Time{}
	hidden := a.hidden
	a.mu.Unlock()
	if !hidden {
		ensureTopmost(h)
	}
}

func (a *App) restoreAfterFullscreen(h uintptr) {
	wa := getWorkArea()
	dpi := getDPI(h)
	if dpi <= 0 {
		dpi = 96
	}
	a.mu.Lock()
	a.workArea = wa
	a.dpi = float64(dpi) / 96.0
	a.mu.Unlock()
	procSetWindowPos.Call(h, hwndTopmost,
		uintptr(wa.Left), uintptr(wa.Top),
		uintptr(wa.Right-wa.Left), uintptr(wa.Bottom-wa.Top),
		swpNoActivate|swpShowWindow|swpFrameChanged)
	procShowWindow.Call(h, swShowNoActivate)
	a.fixAeroBlur(h) // Win10：全屏避让回来后再清一次毛玻璃层
	a.updateWindowRegion(true)
}

func ensureTopmost(h uintptr) {
	if getExStyle(h)&wsExTopmost != 0 {
		return
	}
	procSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
}

// 优先使用系统通知状态，再用前台窗口几何兜底。
// QUNS_BUSY(2) / QUNS_PRESENTATION_MODE(4) 的命中范围很宽（最大化窗口、专注助手、
// 演示设置都可能返回），单独采信会误隐藏，必须再让前台窗口几何确认一次；
// 只有 QUNS_RUNNING_D3D_FULL_SCREEN(3) 是独占全屏，可以直接采信。
func immersiveFullscreenActive() bool {
	if state, ok := queryUserNotificationState(); ok {
		switch state {
		case 3:
			return true
		case 2, 4:
			return foregroundCoversMonitor()
		}
	}
	return foregroundCoversMonitor()
}

func queryUserNotificationState() (uint32, bool) {
	var state uint32
	r, _, _ := procSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&state)))
	if r != 0 {
		return 0, false
	}
	return state, true
}

func foregroundCoversMonitor() bool {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return false
	}
	var pid uint32
	procGetWindowThreadProcessID.Call(h, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 || pid == uint32(os.Getpid()) {
		return false
	}
	if ok, _, _ := procIsWindowVisible.Call(h); ok == 0 {
		return false
	}
	if ok, _, _ := procIsIconic.Call(h); ok != 0 {
		return false
	}
	return windowCoversMonitor(h)
}

func windowCoversMonitor(h uintptr) bool {
	if procDwmGetWindowAttribute.Find() == nil {
		var cloaked uint32
		procDwmGetWindowAttribute.Call(h, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked))
		if cloaked != 0 {
			return false
		}
	}

	var classBuf [64]uint16
	n, _, _ := procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&classBuf[0])), uintptr(len(classBuf)))
	if n > 0 && int(n) <= len(classBuf) && shellWindowClasses[syscall.UTF16ToString(classBuf[:int(n)])] {
		return false
	}

	// 开启自动隐藏任务栏时，普通最大化窗口也会铺满显示器；带标题栏的最大化窗口不算沉浸式全屏。
	style, _, _ := procGetWindowLongPtrW.Call(h, gwlStyleIndex)
	if zoomed, _, _ := procIsZoomed.Call(h); zoomed != 0 && uint32(style)&wsCaption == wsCaption {
		return false
	}

	var wr RECT
	if ok, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr))); ok == 0 {
		return false
	}
	mon, _, _ := procMonitorFromWindow.Call(h, monitorDefaultToNearest)
	if mon == 0 {
		return false
	}
	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return false
	}
	const tol int32 = 2
	m := mi.RcMonitor
	return wr.Left <= m.Left+tol && wr.Top <= m.Top+tol &&
		wr.Right >= m.Right-tol && wr.Bottom >= m.Bottom-tol
}
