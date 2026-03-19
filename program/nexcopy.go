package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// ---------- ITEM & SSH LIST ----------

type item struct {
	name  string
	isDir bool
	size  int64
}

func (i item) Title() string       { return i.name }
func (i item) Description() string { return "" }
func (i item) FilterValue() string { return i.name }

func listRemote(user, host, path string) ([]list.Item, error) {
	cmd := exec.Command("ssh", user+"@"+host, "ls -l "+path)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(out), "\n")
	var items []list.Item
	for _, l := range lines {
		if l == "" || strings.HasPrefix(l, "total") {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) < 9 {
			continue
		}
		isDir := strings.HasPrefix(fields[0], "d")
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		name := strings.Join(fields[8:], " ")
		items = append(items, item{name: name, isDir: isDir, size: size})
	}
	return items, nil
}

// ---------- WINDOWS DIALOG HELPERS (WITH DEFAULT PATHS) ----------

// getDownloadsPath returns the Windows Downloads folder path
func getDownloadsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

// openFolderDialog opens a native Windows folder selection dialog starting in Downloads.
func openFolderDialog(title string) (string, error) {
	dlPath := getDownloadsPath()
	// PowerShell: We use the Shell.Application BrowseForFolder to allow a specific root string path
	psScript := fmt.Sprintf(`
	Add-Type -AssemblyName System.Windows.Forms
	$f = New-Object System.Windows.Forms.FolderBrowserDialog
	$f.Description = "%s"
	$f.SelectedPath = "%s"
	$f.ShowNewFolderButton = $true
	if($f.ShowDialog() -eq "OK"){
		Write-Output $f.SelectedPath
	}`, title, dlPath)

	out, err := exec.Command("powershell", "-Command", psScript).Output()
	if err != nil {
		return "", err
	}

	path := strings.TrimSpace(string(out))
	return path, nil
}

// openFileDialog opens a native Windows dialog for multiple FILES starting in Downloads.
func openFileDialog(title string) ([]string, error) {
	dlPath := getDownloadsPath()
	psScript := fmt.Sprintf(`
	Add-Type -AssemblyName System.Windows.Forms
	$f = New-Object System.Windows.Forms.OpenFileDialog
	$f.Title = "%s"
	$f.InitialDirectory = "%s"
	$f.Multiselect = $true
	if($f.ShowDialog() -eq "OK"){
		$f.FileNames
	}`, title, dlPath)

	out, err := exec.Command("powershell", "-Command", psScript).Output()
	if err != nil {
		return nil, err
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\r\n"), nil
}

// ---------- MODEL & TUI ----------

type model struct {
	list     list.Model
	selected map[string]bool
	user     string
	host     string
	path     string
	dest     string
	status   string
}

type delegate struct {
	m *model
}

func (d delegate) Height() int                               { return 1 }
func (d delegate) Spacing() int                              { return 0 }
func (d delegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d delegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it := listItem.(item)
	cursor := " "
	if index == m.Index() {
		cursor = ">"
	}
	prefix := " "
	if d.m.selected[it.name] {
		prefix = "x"
	}
	fmt.Fprintf(w, "%s [%s] %-25s (%d bytes)", cursor, prefix, it.name, it.size)
}

func initialModel(items []list.Item, user, host, path, dest string) model {
	m := model{
		selected: make(map[string]bool),
		user:     user,
		host:     host,
		path:     path,
		dest:     dest,
		status:   "ready",
	}
	m.list = list.New(items, delegate{m: &m}, 80, 20)
	m.list.Title = "Remote files"
	return m
}

func (m model) Init() tea.Cmd { return nil }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(item); ok {
				m.selected[it.name] = !m.selected[it.name]
			}
		case "c":
			return m, tea.Quit
		case "q":
			os.Exit(0)
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) View() string {
	return fmt.Sprintf("%s\n\nSelected: %d | Dest: %s\n[enter] toggle  [c] confirm copy  [q] quit",
		m.list.View(), len(m.selected), m.dest)
}

func (m model) copyCmd() {
	for name, isSelected := range m.selected {
		if !isSelected {
			continue
		}
		remote := fmt.Sprintf("%s@%s:%s/%s", m.user, m.host, m.path, name)
		cmd := exec.Command("scp", "-r", remote, m.dest)
		fmt.Printf("Copying %s...\n", name)
		cmd.Run()
	}
}

// ---------- MAIN ----------

func main() {
	user := ""       //PUT YOUR USERNAME HERE
	host := ""       //PUT YOUR IP HERE
	remotePath := "" //PUT YOUR REMOTE PATH HERE

	modeDown := flag.Bool("down", false, "Download from remote")
	modeUp := flag.Bool("up", false, "Upload to remote")
	flag.Parse()

	// -------- DOWNLOAD LOGIC --------
	if *modeDown {
		fmt.Println("Select LOCAL folder to save files (starting in Downloads)...")
		dest, err := openFolderDialog("Select Download Destination")
		if err != nil || dest == "" {
			fmt.Println("Cancelled.")
			return
		}

		items, err := listRemote(user, host, remotePath)
		if err != nil {
			fmt.Println("Error listing remote files:", err)
			return
		}

		p := tea.NewProgram(initialModel(items, user, host, remotePath, dest))
		finalModel, _ := p.Run()

		if m, ok := finalModel.(model); ok {
			m.copyCmd()
			fmt.Println("Download complete!")
		}
		return
	}

	// -------- UPLOAD LOGIC --------
	if *modeUp {
		fmt.Print("Upload [f]iles or a [d]irectory? (f/d): ")
		var choice string
		fmt.Scanln(&choice)

		var paths []string
		var err error
		choice = strings.ToLower(strings.TrimSpace(choice))

		if choice == "d" {
			folderPath, errDialog := openFolderDialog("Select Folder to Upload")
			err = errDialog
			if folderPath != "" {
				paths = append(paths, folderPath)
			}
		} else {
			paths, err = openFileDialog("Select Files to Upload")
		}

		if err != nil || len(paths) == 0 {
			fmt.Println("Cancelled.")
			return
		}

		for _, localPath := range paths {
			cleanPath := strings.TrimSpace(localPath)
			if cleanPath == "" {
				continue
			}

			fmt.Printf("Uploading: %s ... ", filepath.Base(cleanPath))
			cmd := exec.Command("scp", "-r", cleanPath, fmt.Sprintf("%s@%s:%s", user, host, remotePath))
			if err := cmd.Run(); err != nil {
				fmt.Printf("FAILED: %v\n", err)
			} else {
				fmt.Println("SUCCESS")
			}
		}
		fmt.Println("\nAll uploads complete.")
		return
	}

	fmt.Println("Use -down or -up flags.")
}
