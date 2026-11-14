//go:build (linux && !android) || windows

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"

	"meshport/internal/agent"
)

var statusLabel = map[string]string{
	"online": "已連線", "connecting": "連線中…", "offline": "離線", "pending": "等待管理員核准",
	"disabled": "裝置已停用", "setup": "尚未設定",
}

func runTray(a *agent.Agent, openUI func()) {
	systray.Run(func() {
		systray.SetIcon(trayIcon(false))
		systray.SetTitle("Meshport")
		systray.SetTooltip("Meshport")
		systray.SetOnTapped(openUI)
		mOpen := systray.AddMenuItem("開啟 Meshport", "開啟主視窗")
		mStatus := systray.AddMenuItem("狀態：啟動中", "")
		mStatus.Disable()
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("結束", "結束 Meshport（停止傳訊與 FRP 映射）")
		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					openUI()
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
		go func() {
			last := ""
			for {
				s := a.Status()
				if s != last {
					last = s
					label := statusLabel[s]
					if label == "" {
						label = s
					}
					mStatus.SetTitle("狀態：" + label)
					systray.SetTooltip("Meshport · " + label)
					systray.SetIcon(trayIcon(s == "online"))
				}
				time.Sleep(2 * time.Second)
			}
		}()
	}, func() {})
}

var (
	iconMu    sync.Mutex
	iconCache = map[bool][]byte{}
)

// trayIcon renders the Meshport mark (blue when online, grey otherwise):
// PNG on Linux, a PNG-in-ICO container on Windows.
func trayIcon(online bool) []byte {
	iconMu.Lock()
	defer iconMu.Unlock()
	if b, ok := iconCache[online]; ok {
		return b
	}
	bg := color.RGBA{0x2f, 0x6f, 0xed, 0xff}
	if !online {
		bg = color.RGBA{0x7a, 0x84, 0x99, 0xff}
	}
	b := renderIcon(64, bg)
	if runtime.GOOS == "windows" {
		b = wrapICO(b, 64)
	}
	iconCache[online] = b
	return b
}

func renderIcon(size int, bg color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size) / 64
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	nodes := [][2]float64{{18, 44}, {32, 20}, {46, 44}}
	edges := [][2]int{{0, 1}, {1, 2}, {0, 2}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := (float64(x)+0.5)/s, (float64(y)+0.5)/s
			if !inRoundRect(px, py, 64, 14) {
				continue
			}
			c := bg
			for _, e := range edges {
				a, b := nodes[e[0]], nodes[e[1]]
				if segDist(px, py, a[0], a[1], b[0], b[1]) <= 2.2 {
					c = white
				}
			}
			for _, n := range nodes {
				if math.Hypot(px-n[0], py-n[1]) <= 6 {
					c = white
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func inRoundRect(x, y, size, r float64) bool {
	cx := math.Max(r, math.Min(size-r, x))
	cy := math.Max(r, math.Min(size-r, y))
	return math.Hypot(x-cx, y-cy) <= r
}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// wrapICO puts one PNG image into an .ico container (Windows Vista+).
func wrapICO(pngData []byte, size int) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1})
	dim := byte(size)
	if size >= 256 {
		dim = 0
	}
	b.Write([]byte{dim, dim, 0, 0})
	_ = binary.Write(&b, binary.LittleEndian, []uint16{1, 32})
	_ = binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(pngData)), 22})
	b.Write(pngData)
	return b.Bytes()
}
