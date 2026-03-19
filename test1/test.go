package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type item struct {
	name  string
	isDir bool
	size  int64
}

func (i item) Title() string       { return i.name }
func (i item) Description() string { return "" }
func (i item) FilterValue() string { return i.name }

// ---------- SSH LIST ----------

func listRemote(user, host, path string) ([]list.Item, error) {
	log.Printf("listRemote called with user: %s, host: %s, path: %s", user, host, path)
	cmd := exec.Command("ssh", user+"@"+host, "ls -l "+path)
	log.Printf("Executing command: %s", cmd.String())

	out, err := cmd.Output()
	if err != nil {
		log.Printf("listRemote command failed: %v", err)
		return nil, err
	}

	lines := strings.Split(string(out), "\n")
	log.Printf("listRemote returned %d lines of output", len(lines))
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

		items = append(items, item{
			name:  name,
			isDir: isDir,
			size:  size,
		})
	}

	log.Printf("listRemote parsed %d valid items", len(items))
	return items, nil
}

// ---------- MODEL ----------

type model struct {
	list     list.Model
	selected map[string]bool

	user string
	host string
	path string
	dest string

	status string
}

// ---------- DELEGATE ----------

type delegate struct {
	m *model
}

func (d delegate) Height() int  { return 1 }
func (d delegate) Spacing() int { return 0 }

func (d delegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	return nil
}

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

	typ := "file"
	if it.isDir {
		typ = "dir"
	}

	fmt.Fprintf(w, "%s [%s] %-25s (%s, %d bytes)",
		cursor,
		prefix,
		it.name,
		typ,
		it.size,
	)
}

// ---------- INIT ----------

func initialModel(items []list.Item, user, host, path, dest string) model {
	log.Printf("Initializing model with %d items, dest: %s", len(items), dest)
	m := model{
		selected: make(map[string]bool),
		user:     user,
		host:     host,
		path:     path,
		dest:     dest,
		status:   "ready",
	}

	d := delegate{m: &m}
	l := list.New(items, d, 80, 20)
	l.Title = "Remote files"

	m.list = l
	return m
}

// ---------- BUBBLETEA ----------

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case string:
		log.Printf("Update received string msg: %s", msg)
		m.status = msg
		return m, nil

	case tea.KeyMsg:
		log.Printf("Update received KeyMsg: %s", msg.String())
		switch msg.String() {

		case "enter":
			if it, ok := m.list.SelectedItem().(item); ok {
				m.selected[it.name] = !m.selected[it.name]
				log.Printf("Toggled selection for %s: %v", it.name, m.selected[it.name])
			}

		case "c":
			log.Println("Initiating copy sequence, exiting TUI loop...")
			m.status = "exiting..."
			return m, tea.Quit

		case "q":
			log.Println("User quit the application")
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// ---------- COPY ----------

func (m model) copyCmd() tea.Cmd {
	return func() tea.Msg {
		log.Printf("Executing copyCmd for %d selected items", len(m.selected))
		for name, isSelected := range m.selected {
			if !isSelected {
				continue
			}
			remote := fmt.Sprintf("%s@%s:%s/%s", m.user, m.host, m.path, name)

			cmd := exec.Command("scp", "-r", remote, m.dest)
			log.Printf("Executing SCP: %s", cmd.String())

			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Printf("scp failed for %s: %v\nOutput: %s", name, err, string(out))
				return fmt.Sprintf("scp failed: %v\n%s", err, string(out))
			}
			log.Printf("Successfully copied %s to %s", name, m.dest)
		}
		return "copy done"
	}
}

// ---------- VIEW ----------

func (m model) View() string {
	return fmt.Sprintf(
		"%s\n\nSelected: %d\nStatus: %s\n\n[enter] toggle  [c] copy  [q] quit",
		m.list.View(),
		len(m.selected),
		m.status,
	)
}

// ---------- HELPERS ----------

func contains(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

// getWindowsDownloadsPath queries the Windows Shell for the actual Downloads folder location.
func getWindowsDownloadsPath() string {
	// Query PowerShell for the official Downloads folder path (handles relocated folders)
	cmd := exec.Command("powershell", "-Command", "(New-Object -ComObject Shell.Application).NameSpace('shell:Downloads').Self.Path")
	out, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to query Windows for Downloads path: %v", err)
		// Fallback to standard C:\Users\...\Downloads
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Downloads")
	}
	return strings.TrimSpace(string(out))
}

// ---------- MAIN ----------

func main() {
	// Initialize logging
	f, err := tea.LogToFile("debug.log", "debug")
	if err != nil {
		fmt.Println("fatal:", err)
		os.Exit(1)
	}
	defer f.Close()
	log.Println("--- Application Started ---")

	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	downDest := fs.String("down", "", "download destination")
	upSource := fs.String("up", "", "upload source folder")

	_ = fs.Parse(os.Args[1:])

	// Fetch the actual Downloads path from Windows settings
	actualDownloads := getWindowsDownloadsPath()
	log.Printf("Detected Windows Downloads path: %s", actualDownloads)

	user := "dovshmi"
	host := "192.168.1.147"
	remotePath := "/home/dovshmi/Downloads"

	// -------- DOWNLOAD --------
	if *downDest != "" || contains(os.Args, "-down") {
		log.Println("Mode: Download detected")
		dest := *downDest
		if dest == "" {
			dest = actualDownloads
			fmt.Printf("Defaulting to Windows Downloads folder: %s\n", dest)
		}

		// Ensure the directory exists
		if err := os.MkdirAll(dest, os.ModePerm); err != nil {
			log.Printf("Error creating directory %s: %v", dest, err)
			fmt.Println("Error:", err)
			return
		}

		items, err := listRemote(user, host, remotePath)
		if err != nil {
			log.Printf("Fatal error in listRemote: %v", err)
			fmt.Println("error:", err)
			return
		}

		p := tea.NewProgram(initialModel(items, user, host, remotePath, dest))
		log.Println("Starting Bubble Tea program...")

		finalModel, _ := p.Run()
		if m, ok := finalModel.(model); ok {
			msg := m.copyCmd()()
			fmt.Println(msg)
			log.Printf("Copy finished with message: %s", msg)
		}
		return
	}

	// -------- UPLOAD --------
	if *upSource != "" || contains(os.Args, "-up") {
		log.Println("Mode: Upload detected")
		if *upSource == "" {
			*upSource = actualDownloads
			fmt.Printf("Uploading from Windows Downloads folder: %s\n", *upSource)
		}

		cmd := exec.Command("scp", "-r", *upSource,
			fmt.Sprintf("%s@%s:%s", user, host, remotePath),
		)
		log.Printf("Executing upload SCP: %s", cmd.String())

		fmt.Println("uploading...")
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("Upload SCP failed: %v\nOutput: %s", err, string(out))
			fmt.Println("upload error:", string(out))
			return
		}

		fmt.Println("upload done")
		return
	}

	fmt.Println("default mode")
}
