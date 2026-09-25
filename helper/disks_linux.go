//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
)

type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Size        int64      `json:"size"`
	Model       *string    `json:"model"`
	Serial      *string    `json:"serial"`
	Tran        *string    `json:"tran"`
	RM          bool       `json:"rm"`
	Type        string     `json:"type"`
	Mountpoints []*string  `json:"mountpoints"`
	Children    []lsblkDev `json:"children"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func hasSystemMount(d lsblkDev) bool {
	for _, m := range d.Mountpoints {
		if m != nil && (*m == "/" || *m == "/boot" || *m == "/boot/efi" || *m == "/usr" || strings.HasPrefix(*m, "[SWAP]")) {
			return true
		}
	}
	for _, c := range d.Children {
		if hasSystemMount(c) {
			return true
		}
	}
	return false
}

func osListDisks() ([]Disk, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,MODEL,SERIAL,TRAN,RM,TYPE,MOUNTPOINTS").Output()
	if err != nil {
		return nil, err
	}
	var r struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	var disks []Disk
	for _, d := range r.Blockdevices {
		if d.Type != "disk" || hasSystemMount(d) {
			continue
		}
		if str(d.Tran) != "usb" && !d.RM {
			continue
		}
		disks = append(disks, Disk{
			ID: d.Path, Path: d.Path, Size: d.Size, Bus: strings.ToUpper(str(d.Tran)),
			Name: strings.TrimSpace(str(d.Model)), Serial: str(d.Serial),
		})
	}
	return disks, nil
}

func osPrepareDisk(d Disk) error {
	out, _ := exec.Command("lsblk", "-ln", "-o", "PATH,MOUNTPOINT", d.Path).Output()
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 {
			if err := exec.Command("umount", f[0]).Run(); err != nil {
				return errors.New("não foi possível desmontar " + f[0])
			}
		}
	}
	return nil
}

func osOpenDevice(d Disk) (*os.File, error) { return os.OpenFile(d.Path, os.O_RDWR|os.O_SYNC, 0) }

func osFinalize(d Disk) { _ = exec.Command("sync").Run() }

func osEnsureElevated() (bool, error) {
	if os.Geteuid() != 0 && os.Getenv("UAB_FAKE_DISK") == "" {
		return false, errors.New("no Linux, execute com sudo: sudo ./gravador")
	}
	return false, nil
}

func osOpenBrowser(url string) { _ = exec.Command("xdg-open", url).Start() }

func osNeedsPrivilegedWorker() bool { return false }

func osRunPrivileged(args []string) error { return errors.New("não suportado") }
