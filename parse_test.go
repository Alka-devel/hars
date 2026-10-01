package ruwiki

import (
	"os"
	"strings"
	"testing"
)

func TestSearchTerm(t *testing.T) {
	browser, brrErr := StartChrome()
	if brrErr != nil {
		t.Fatal(brrErr)
	}
	defer browser.Close()
	term := "чат-бот"
	if len(os.Args) > 1 {
		term = strings.Join(os.Args[1:], " ")
	}
	ti, te, e := SearchTerm(term)
	if e != nil {
		t.Fatal(e)
	}
	t.Log(ti, "\n", te)
}
