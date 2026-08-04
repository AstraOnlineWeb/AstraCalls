package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Clientes como o n8n costumam mandar campos numéricos como STRING ("49.9"). Os
// tipos abaixo aceitam tanto número JSON quanto string numérica, evitando o erro
// "invalid body" no decode. Também aceitam vírgula decimal ("49,9").

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		str = strings.ReplaceAll(strings.TrimSpace(str), ",", ".")
		if str == "" {
			return nil
		}
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return err
		}
		*f = flexFloat(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

func (f flexFloat) Float() float64 { return float64(f) }

type flexInt int

func (i *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		if str == "" {
			return nil
		}
		// aceita "3000" ou "3000.0"
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return err
		}
		*i = flexInt(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*i = flexInt(v)
	return nil
}

func (i flexInt) Int() int { return int(i) }
