package main

import (
	"reflect"
	"testing"
)

func TestForwardServerFlagToUI(t *testing.T) {
	got := forwardServerFlag([]string{"--port", "7601", "--open=false"}, "http://coordinator:7600")
	want := []string{"--port", "7601", "--open=false", "--server", "http://coordinator:7600"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forwardServerFlag() = %v, want %v", got, want)
	}
}

func TestForwardServerFlagToUINoGlobalServer(t *testing.T) {
	args := []string{"--port", "7601"}
	got := forwardServerFlag(args, "")
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("forwardServerFlag() = %v, want %v", got, args)
	}
}
