//go:build darwin

package main

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
)

func osVentoy(d Disk) (*VentoyInfo, error) {
	out, err := exec.Command("diskutil", "list", "-plist", d.ID).Output()
	if err != nil {
		return nil, err
	}
	root, err := parsePlist(out)
	if err != nil {
		return nil, err
	}
	var parts []map[string]any
	for _, x := range asList(root["AllDisksAndPartitions"]) {
		m, _ := x.(map[string]any)
		for _, p := range asList(m["Partitions"]) {
			if pm, ok := p.(map[string]any); ok {
				parts = append(parts, pm)
			}
		}
	}
	has := false
	for _, p := range parts {
		if n, _ := p["VolumeName"].(string); strings.EqualFold(n, "VTOYEFI") {
			has = true
		}
	}
	if !has || len(parts) == 0 {
		return nil, nil
	}
	id, _ := parts[0]["DeviceIdentifier"].(string)
	if id == "" {
		return nil, errors.New("partição das ISOs do Ventoy não encontrada")
	}
	mnt, fs := mountInfo(id)
	var release func()
	if mnt == "" {
		_ = exec.Command("diskutil", "mount", id).Run()
		mnt, fs = mountInfo(id)
		release = func() { _ = exec.Command("diskutil", "unmount", id).Run() }
	}
	if mnt == "" {
		return nil, errors.New("não foi possível montar a partição do Ventoy")
	}
	return &VentoyInfo{Mount: mnt, FS: fs, release: release}, nil
}

func mountInfo(id string) (string, string) {
	out, err := exec.Command("diskutil", "info", "-plist", id).Output()
	if err != nil {
		return "", ""
	}
	m, err := parsePlist(out)
	if err != nil {
		return "", ""
	}
	mp, _ := m["MountPoint"].(string)
	fs, _ := m["FilesystemType"].(string)
	return mp, fs
}

func asList(v any) []any { l, _ := v.([]any); return l }

func freeSpace(path string) int64 {
	var s syscall.Statfs_t
	if syscall.Statfs(path, &s) != nil {
		return 0
	}
	return int64(s.Bavail) * int64(s.Bsize)
}
