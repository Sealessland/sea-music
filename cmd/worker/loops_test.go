package main

import (
	"errors"
	"testing"
)

func TestShouldImmediatelyDispatchAgain(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		dispatched int
		err        error
		want       bool
	}{
		{name: "made progress", dispatched: 1, want: true},
		{name: "empty backlog", dispatched: 0, want: false},
		{name: "failed batch", dispatched: 1, err: errors.New("publish failed"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldImmediatelyDispatchAgain(test.dispatched, test.err); got != test.want {
				t.Fatalf("shouldImmediatelyDispatchAgain(%d, %v) = %t, want %t", test.dispatched, test.err, got, test.want)
			}
		})
	}
}
