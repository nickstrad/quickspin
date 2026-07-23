package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nickstrad/quickspin/internal/greeting"
)

func main() {
	name := strings.Join(os.Args[1:], " ")
	fmt.Println(greeting.Greet(name))
}
