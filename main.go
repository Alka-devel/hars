package main

import "fmt"

func main() {
	a, b, c := searchTerm("Какашка")
	if c != nil {
		panic(c)
	}
	fmt.Println(a, "\n", b)
}
