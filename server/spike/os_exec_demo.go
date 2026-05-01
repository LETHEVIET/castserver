//go:build ignore

package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os/exec"
)

func main() {
	ctx := context.Background()

	cmd := exec.CommandContext(ctx, "echo", "hello from subprocess")

	stdout, err := cmd.StdoutPipe()

	if err != nil {
		log.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		fmt.Println("GOT:", scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		log.Fatal(err)
	}
}
