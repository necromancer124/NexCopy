package main

import (
	"errors"
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

func getDownloadsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func openFolderDialog(title string) (string, error) {
	dlPath := getDownloadsPath()
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

	return strings.TrimSpace(string(out)), nil
}

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

// ---------- DOWNLOAD TUI MODEL ----------

type downloadModel struct {
	list     list.Model
	selected map[string]bool
	user     string
	host     string
	path     string
	dest     string
	status   string
}

type downloadDelegate struct {
	m *downloadModel
}

func (d downloadDelegate) Height() int                               { return 1 }
func (d downloadDelegate) Spacing() int                              { return 0 }
func (d downloadDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d downloadDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
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

func initialDownloadModel(items []list.Item, user, host, path, dest string) downloadModel {
	m := downloadModel{
		selected: make(map[string]bool),
		user:     user,
		host:     host,
		path:     path,
		dest:     dest,
		status:   "ready",
	}
	m.list = list.New(items, downloadDelegate{m: &m}, 80, 20)
	m.list.Title = "Remote files"
	return m
}

func (m downloadModel) Init() tea.Cmd { return nil }
func (m downloadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(item); ok {
				m.selected[it.name] = !m.selected[it.name]
			}
		case "c":
			return m, tea.Quit
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m downloadModel) View() string {
	return fmt.Sprintf("%s\n\nSelected: %d | Dest: %s\n[enter] toggle  [c] confirm copy  [q] cancel/quit",
		m.list.View(), len(m.selected), m.dest)
}

func (m downloadModel) copyCmd() {
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

// ---------- MAIN MENU TUI MODEL ----------

type menuChoice string

const (
	choiceDownload menuChoice = "download"
	choiceUpload   menuChoice = "upload"
	choiceQuit     menuChoice = "quit"
)

type menuItem struct {
	title, desc string
	choice      menuChoice
}

func (i menuItem) Title() string       { return i.title }
func (i menuItem) Description() string { return i.desc }
func (i menuItem) FilterValue() string { return i.title }

type menuModel struct {
	list     list.Model
	selected menuChoice
}

func initialMenuModel() menuModel {
	items := []list.Item{
		menuItem{title: "Download from remote", desc: "Select remote files to download to your PC", choice: choiceDownload},
		menuItem{title: "Upload to remote", desc: "Select local files/folders to upload to the server", choice: choiceUpload},
		menuItem{title: "Quit", desc: "Exit the application", choice: choiceQuit},
	}
	m := menuModel{
		list:     list.New(items, list.NewDefaultDelegate(), 60, 15),
		selected: choiceQuit, // Default safely to quit
	}
	m.list.Title = "File Transfer Menu"
	m.list.SetShowStatusBar(false)
	m.list.SetFilteringEnabled(false)
	return m
}

func (m menuModel) Init() tea.Cmd { return nil }

func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.selected = choiceQuit
			return m, tea.Quit
		case "enter":
			if i, ok := m.list.SelectedItem().(menuItem); ok {
				m.selected = i.choice
			}
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m menuModel) View() string {
	return "\n" + m.list.View()
}

// ---------- LOGIC HANDLERS ----------

func handleDownload(user, host, remotePath string) {
	fmt.Println("\n[Download] Opening folder dialog to select save location...")
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

	p := tea.NewProgram(initialDownloadModel(items, user, host, remotePath, dest))
	finalModel, _ := p.Run()

	if m, ok := finalModel.(downloadModel); ok {
		// Only run the copy command if there are actually files selected
		hasSelections := false
		for _, v := range m.selected {
			if v {
				hasSelections = true
				break
			}
		}

		if hasSelections {
			m.copyCmd()
			fmt.Println("Download complete!")
		} else {
			fmt.Println("No files selected for download.")
		}
	}
}

func handleUpload(user, host, remotePath string) {
	fmt.Print("\nUpload [f]iles or a [d]irectory? (f/d): ")
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
}

// ---------- ERROR CHECKING FOR CONFIGURATION ----------
func validateConfig(user, host, path string) error {
	if user == "" || host == "" || path == "" {
		return errors.New("missing required environment variables (user, host, or remotePath)")
	}
	return nil
}

// ---------- MAIN ----------

func main() {
	user := ""       //PUT YOUR USERNAME HERE
	host := ""       //PUT YOUR IP HERE
	remotePath := "" //PUT YOUR REMOTE PATH HERE
	if user == "" || host == "" || remotePath == "" {
		// \033[31m is the ANSI code for Red, \033[0m resets it
		fmt.Fprintf(os.Stderr, "\033[31mConfiguration Error: user, host, and remotePath must be set in main() before running.\033[0m\n")
		os.Exit(1)
	}
	for {
		// Run the main menu TUI
		p := tea.NewProgram(initialMenuModel())
		finalModel, err := p.Run()
		if err != nil {
			fmt.Printf("Alas, there's been an error: %v", err)
			os.Exit(1)
		}

		// Handle the user's choice from the menu
		if m, ok := finalModel.(menuModel); ok {
			switch m.selected {
			case choiceDownload:
				handleDownload(user, host, remotePath)
			case choiceUpload:
				handleUpload(user, host, remotePath)
			case choiceQuit:
				fmt.Println("Goodbye!")
				return
			}
		}

		// Pause briefly to let the user read success/error messages before returning to the menu
		fmt.Println("\nPress [Enter] to return to the main menu...")
		fmt.Scanln()
	}
}
