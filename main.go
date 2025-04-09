package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

//go:generate fyne package -os windows -icon sync.png
//go:generate upx -9  syncfolders.exe

type SyncStats struct {
	TotalFiles     int
	CopiedFiles    int
	DeletedFiles   int
	CurrentFile    string
	Speed          float64
	BytesCopied    int64
	StartTime      time.Time
	LastUpdateTime time.Time
	LastBytes      int64
	ClearTarget    bool
	cur            atomic.Bool
}

func main() {
	stats := &SyncStats{cur: atomic.Bool{}}

	myApp := app.New()
	window := myApp.NewWindow("文件夹同步工具")

	// UI组件
	sourceEntry := widget.NewEntry()
	sourceEntry.SetPlaceHolder("源文件夹路径")
	targetEntry := widget.NewEntry()
	targetEntry.SetPlaceHolder("目标文件夹路径")

	clearTarget := widget.NewCheck("是否删除目标文件夹多余的文件", nil)
	clearTarget.SetChecked(stats.ClearTarget)

	progressBar := widget.NewProgressBar()
	progressBar.Min = 0
	progressBar.Max = 100

	statusLabel := widget.NewLabel("准备同步...")
	currentFileLabel := widget.NewLabel("")
	speedLabel := widget.NewLabel("")
	statsLabel := widget.NewLabel("")

	var startButton *widget.Button

	startButton = widget.NewButton("开始同步", func() {
		startButton.Disabled()
		source := sourceEntry.Text
		target := targetEntry.Text

		statusLabel.SetText("")
		currentFileLabel.SetText("")
		speedLabel.SetText("")
		statsLabel.SetText("")
		progressBar.SetValue(0)

		if source == "" || target == "" {
			statusLabel.SetText("错误: 请指定源文件夹和目标文件夹")
			return
		}

		// 更新UI的goroutine
		go func() {
			for {
				if stats.TotalFiles > 0 {
					progress := float64(stats.CopiedFiles+stats.DeletedFiles) / float64(stats.TotalFiles) * 100
					progressBar.SetValue(progress)
					statusLabel.SetText(fmt.Sprintf("正在同步: %d/%d 文件", stats.CopiedFiles+stats.DeletedFiles, stats.TotalFiles))
					currentFileLabel.SetText("当前文件: " + stats.CurrentFile)
					speedLabel.SetText(fmt.Sprintf("速度: %.2f MB/s", stats.Speed))
					statsLabel.SetText(fmt.Sprintf("已复制: %d 文件, 已删除: %d 文件", stats.CopiedFiles, stats.DeletedFiles))
				}
				time.Sleep(100 * time.Millisecond)
				if progressBar.Value >= 100 {
					break
				}
			}
			statusLabel.SetText("同步完成!")
		}()

		// 执行同步的goroutine
		go func() {
			if stats.cur.Load() {
				return
			}
			defer startButton.Enable()
			stats.cur.Store(true)
			defer stats.cur.Store(false)
			defer progressBar.SetValue(100)
			stats.TotalFiles = 0
			stats.BytesCopied = 0
			stats.StartTime = time.Now()
			stats.LastBytes = 0
			stats.CopiedFiles = 0
			stats.DeletedFiles = 0
			stats.CurrentFile = ""
			stats.Speed = 0
			stats.ClearTarget = clearTarget.Checked
			err := syncFolders(source, target, stats)
			if err != nil {
				statusLabel.SetText("错误: " + err.Error())
				fmt.Println(err)
			}

		}()
	})

	browseSource := widget.NewButton("浏览...", func() {

		fileOpen := dialog.NewFolderOpen(func(reader fyne.ListableURI, err error) {
			if err == nil && reader != nil {
				sourceEntry.SetText(reader.Path())
			}
		}, window)
		fileOpen.SetFilter(storageFilter)
		fileOpen.Show()
	})

	browseTarget := widget.NewButton("浏览...", func() {
		fileOpen := dialog.NewFolderOpen(func(reader fyne.ListableURI, err error) {
			if err == nil && reader != nil {
				targetEntry.SetText(reader.Path())
			}
		}, window)
		fileOpen.SetFilter(storageFilter)
		fileOpen.Show()
	})

	// 布局
	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "源文件夹", Widget: container.NewGridWithRows(2, sourceEntry, browseSource)},
			{Text: "目标文件夹", Widget: container.NewGridWithRows(2, targetEntry, browseTarget)},
			{Text: "多余文件删除", Widget: clearTarget},
		},
	}

	content := container.NewVBox(
		form,
		startButton,
		progressBar,
		statusLabel,
		currentFileLabel,
		speedLabel,
		statsLabel,
	)

	window.SetContent(content)
	window.Resize(fyne.NewSize(600, 400))
	window.ShowAndRun()
}

var storageFilter = storage.NewExtensionFileFilter([]string{".*"})

func syncFolders(source, target string, stats *SyncStats) error {
	// 首先统计总文件数
	totalFiles := 0
	filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			totalFiles++
		}
		return nil
	})
	filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			totalFiles++
		}
		return nil
	})

	stats.TotalFiles = totalFiles

	// 同步源文件到目标
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(target, relPath)

		stats.CurrentFile = relPath

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		} else {
			err := copyFileIfNeeded(path, targetPath, info, stats)
			if err == nil {
				stats.CopiedFiles++
			}
			return err
		}
	})

	if err != nil {
		return fmt.Errorf("error walking source directory: %v", err)
	}
	// 清理目标文件夹
	if stats.ClearTarget {
		err = cleanTarget(source, target, stats)
		if err != nil {
			return fmt.Errorf("error cleaning target directory: %v", err)
		}
	}
	return nil
}

func copyFileIfNeeded(source, target string, sourceInfo os.FileInfo, stats *SyncStats) error {
	targetInfo, err := os.Stat(target)
	if err == nil {
		if !needCopy(sourceInfo, targetInfo) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	srcFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	dstFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	// 使用带统计的复制
	copier := &statsWriter{
		writer: dstFile,
		stats:  stats,
	}

	_, err = io.Copy(copier, srcFile)
	if err != nil {
		return err
	}

	return os.Chmod(target, sourceInfo.Mode())
}

type statsWriter struct {
	writer io.Writer
	stats  *SyncStats
}

func (sw *statsWriter) Write(p []byte) (int, error) {
	n, err := sw.writer.Write(p)
	if n > 0 {
		sw.stats.BytesCopied += int64(n)

		now := time.Now()
		if !sw.stats.LastUpdateTime.IsZero() {
			elapsed := now.Sub(sw.stats.LastUpdateTime).Seconds()
			if elapsed > 0 {
				bytesDiff := sw.stats.BytesCopied - sw.stats.LastBytes
				sw.stats.Speed = (float64(bytesDiff) / 1024 / 1024) / elapsed
			}
		}
		sw.stats.LastUpdateTime = now
		sw.stats.LastBytes = sw.stats.BytesCopied
	}
	return n, err
}

func needCopy(source, target os.FileInfo) bool {
	return source.Size() != target.Size() || source.ModTime().After(target.ModTime())
}

func cleanTarget(source, target string, stats *SyncStats) error {
	// 先收集所有需要删除的项，然后再处理
	var toDelete []string

	err := filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(target, path)
		if err != nil {
			return err
		}

		sourcePath := filepath.Join(source, relPath)
		_, err = os.Stat(sourcePath)
		if os.IsNotExist(err) {
			toDelete = append(toDelete, path)
		} else if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return err
	}

	// 按路径长度从长到短排序，确保先删除子项再删除父目录
	sort.Slice(toDelete, func(i, j int) bool {
		return len(toDelete[i]) > len(toDelete[j])
	})

	// 执行删除操作
	for _, path := range toDelete {
		stats.CurrentFile = "(删除) " + path

		info, err := os.Stat(path)
		if err != nil {
			continue // 可能已经被删除
		}

		if info.IsDir() {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		stats.DeletedFiles++
	}

	return nil
}
