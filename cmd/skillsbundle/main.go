// Command skillsbundle produces the checked-in snapshot assets consumed by a
// CLI or publishable skills plugin. CI resolves a tag or branch to a full SHA
// first; this command accepts only that immutable SHA and performs no network
// access itself.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/producer"
)

var exit = os.Exit
var stderr io.Writer = os.Stderr
var produce = producer.Produce

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("skillsbundle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	descriptorPath := flags.String("descriptor", "", "descriptor JSON path")
	repository := flags.String("repo", "", "local Git repository path")
	output := flags.String("out", "", "fresh output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *descriptorPath == "" || *repository == "" || *output == "" {
		return errors.New("usage: skillsbundle --descriptor bundle.json --repo local-git-repository --out fresh-output-directory")
	}
	descriptor, err := readDescriptor(*descriptorPath)
	if err != nil {
		return err
	}
	result, err := produce(producer.Config{Descriptor: descriptor, RepositoryDir: *repository, OutputDir: *output})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n%s\n%s\n", result.ArchivePath, result.DescriptorPath, result.EmbedDir)
	return err
}

func readDescriptor(name string) (skillsync.BundleDescriptor, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return skillsync.BundleDescriptor{}, fmt.Errorf("read descriptor: %w", err)
	}
	var descriptor skillsync.BundleDescriptor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return skillsync.BundleDescriptor{}, fmt.Errorf("decode descriptor: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return skillsync.BundleDescriptor{}, errors.New("decode descriptor: trailing JSON content")
	}
	return descriptor, nil
}
