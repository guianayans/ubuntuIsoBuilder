package main

// Leitor mínimo de plist XML (saída de `diskutil -plist`). Suporta dict,
// array, string, integer, real, true/false, data e date.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

func parsePlist(b []byte) (map[string]any, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, errors.New("plist inválido")
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			v, err := plistValue(dec, se)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			return m, nil
		}
	}
}

func plistText(dec *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			return sb.String(), nil
		}
	}
}

func plistValue(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					if key, err = plistText(dec); err != nil {
						return nil, err
					}
					continue
				}
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				m[key] = v
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		var a []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			case xml.EndElement:
				return a, nil
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil && err != io.EOF {
			return nil, err
		}
		return se.Name.Local == "true", nil
	case "integer":
		s, err := plistText(dec)
		if err != nil {
			return nil, err
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return n, nil
	case "real":
		s, err := plistText(dec)
		if err != nil {
			return nil, err
		}
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, nil
	default: // string, data, date
		return plistText(dec)
	}
}
