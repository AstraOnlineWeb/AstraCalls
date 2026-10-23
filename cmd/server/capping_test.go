package main

import (
	"encoding/json"
	"testing"
)

func TestParseMessageCapping(t *testing.T) {
	raw := json.RawMessage(`{"xwa2_message_capping_info":{"total_quota":50,"used_quota":"48",
		"cycle_start_timestamp":"1792700000","cycle_end_timestamp":1792786400000,
		"capping_status":"SECOND_WARNING","ote_status":"ELIGIBLE","mv_status":"NOT_ACTIVE",
		"subscription_status":{"status":"NONE","name":""}}}`)
	c, err := parseMessageCapping(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.TotalQuota != 50 || c.UsedQuota != 48 || c.Remaining != 2 || c.Status != "SECOND_WARNING" {
		t.Fatalf("cota/status errados: %+v", c)
	}
	if c.CycleStartAt != 1792700000000 || c.CycleEndAt != 1792786400000 {
		t.Fatalf("timestamps: %d %d", c.CycleStartAt, c.CycleEndAt)
	}
	if c.SubscriptionStatus != "NONE" || c.OTEStatus != "ELIGIBLE" {
		t.Fatalf("extras: %+v", c)
	}

	// resposta real das contas de teste: cota -1 = sem limite aplicado
	c, err = parseMessageCapping(json.RawMessage(`{"xwa2_message_capping_info":{"total_quota":-1,"used_quota":0,"capping_status":"NONE"}}`))
	if err != nil || !c.Unlimited || c.Remaining != -1 {
		t.Fatalf("sem limite: %+v %v", c, err)
	}

	c, err = parseMessageCapping(json.RawMessage(`{"xwa2_message_capping_info":{}}`))
	if err != nil || c.Status != "NONE" || c.Remaining != 0 {
		t.Fatalf("vazio: %+v %v", c, err)
	}
	if _, err := parseMessageCapping(json.RawMessage(`{}`)); err == nil {
		t.Fatal("sem payload deveria dar erro")
	}
}
