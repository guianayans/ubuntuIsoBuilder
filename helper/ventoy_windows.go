//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

func osVentoy(d Disk) (*VentoyInfo, error) {
	out, err := ps(fmt.Sprintf(`Get-Partition -DiskNumber %s | ForEach-Object { $v = $_ | Get-Volume -ErrorAction SilentlyContinue; [pscustomobject]@{N=$_.PartitionNumber; L=[string]$_.DriveLetter; Label=[string]$v.FileSystemLabel; FS=[string]$v.FileSystem} } | ConvertTo-Json -Compress`, d.ID))
	if err != nil {
		return nil, err
	}
	var parts []struct{ N int; L, Label, FS string }
	s := strings.TrimSpace(string(out))
	if strings.HasPrefix(s, "{") {
		s = "[" + s + "]"
	}
	if json.Unmarshal([]byte(s), &parts) != nil {
		return nil, nil
	}
	has := false
	for _, p := range parts {
		if strings.EqualFold(p.Label, "VTOYEFI") {
			has = true
		}
	}
	if !has {
		return nil, nil
	}
	for _, p := range parts {
		if p.N == 1 && !strings.EqualFold(p.Label, "VTOYEFI") {
			if p.L == "" || p.L == "\x00" {
				return nil, fmt.Errorf("a partição das ISOs do Ventoy está sem letra de unidade")
			}
			return &VentoyInfo{Mount: p.L + `:\`, FS: strings.ToLower(p.FS)}, nil
		}
	}
	return nil, fmt.Errorf("partição das ISOs do Ventoy não encontrada")
}

func freeSpace(path string) int64 {
	var free, total, avail uint64
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return 0
	}
	return int64(avail)
}
