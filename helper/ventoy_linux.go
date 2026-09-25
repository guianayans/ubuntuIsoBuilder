//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// osVentoy: VTOYEFI em alguma partição = pendrive com Ventoy. A partição das
// ISOs (a outra, a primeira) é montada se ainda não estiver.
func osVentoy(d Disk) (*VentoyInfo, error) {
	// com NAME o lsblk devolve a árvore (partições em "children"); sem árvore, vêm
	// no mesmo nível do disco: aceita os dois formatos
	out, err := exec.Command("lsblk", "-J", "-o", "NAME,PATH,LABEL,FSTYPE,MOUNTPOINT,TYPE", d.Path).Output()
	if err != nil {
		return nil, err
	}
	type dev struct {
		Path, Label, Fstype, Mountpoint, Type *string
		Children []dev `json:"children"`
	}
	var r struct {
		Blockdevices []dev `json:"blockdevices"`
	}
	if json.Unmarshal(out, &r) != nil || len(r.Blockdevices) == 0 {
		return nil, errors.New("lsblk sem resposta")
	}
	var parts []dev
	for _, b := range r.Blockdevices {
		if str(b.Type) == "part" {
			parts = append(parts, b)
		}
		for _, c := range b.Children {
			if str(c.Type) == "part" {
				parts = append(parts, c)
			}
		}
	}
	// logo depois de conectar, o udev pode ainda não ter lido os rótulos: lê do disco
	for i := range parts {
		if str(parts[i].Label) == "" || str(parts[i].Fstype) == "" {
			l := blkidValue(str(parts[i].Path), "LABEL")
			t := blkidValue(str(parts[i].Path), "TYPE")
			parts[i].Label, parts[i].Fstype = &l, &t
		}
	}
	has := false
	for _, p := range parts {
		if strings.EqualFold(str(p.Label), "VTOYEFI") {
			has = true
		}
	}
	if !has || len(parts) == 0 {
		return nil, nil
	}
	data := parts[0]
	if strings.EqualFold(str(data.Label), "VTOYEFI") {
		return nil, errors.New("partição das ISOs do Ventoy não encontrada")
	}
	mnt := str(data.Mountpoint)
	if mnt == "" { // não montada: monta numa pasta temporária (o isoBuilder roda como root no Linux)
		dir, err := os.MkdirTemp("", "isobuilder-ventoy")
		if err != nil {
			return nil, err
		}
		if out, err := exec.Command("mount", str(data.Path), dir).CombinedOutput(); err != nil {
			os.Remove(dir)
			return nil, errors.New("não foi possível montar a partição do Ventoy: " + strings.TrimSpace(string(out)))
		}
		return &VentoyInfo{Mount: dir, FS: str(data.Fstype), release: func() {
			_ = exec.Command("sync").Run()
			_ = exec.Command("umount", dir).Run()
			_ = os.Remove(dir)
		}}, nil
	}
	return &VentoyInfo{Mount: mnt, FS: str(data.Fstype)}, nil
}

func blkidValue(dev, tag string) string {
	out, err := exec.Command("blkid", "-o", "value", "-s", tag, dev).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func freeSpace(path string) int64 {
	var s syscall.Statfs_t
	if syscall.Statfs(path, &s) != nil {
		return 0
	}
	return int64(s.Bavail) * int64(s.Bsize)
}
