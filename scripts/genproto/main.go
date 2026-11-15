// Command genproto compiles a .proto file with protocompile and runs the pinned
// protoc-gen-go and protoc-gen-go-grpc plugins, so generation needs no protoc install.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	dir := flag.String("dir", ".", "directory holding the .proto file; outputs go next to it")
	file := flag.String("file", "", "proto file name inside -dir")
	flag.Parse()
	if *file == "" {
		fmt.Fprintln(os.Stderr, "genproto: -file is required")
		os.Exit(2)
	}
	if err := run(*dir, *file); err != nil {
		fmt.Fprintln(os.Stderr, "genproto:", err)
		os.Exit(1)
	}
}

func run(dir, file string) error {
	ctx := context.Background()
	compiler := protocompile.Compiler{Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{ImportPaths: []string{dir}})}
	compiled, err := compiler.Compile(ctx, file)
	if err != nil {
		return fmt.Errorf("compiling %s: %w", file, err)
	}
	var files []*descriptorpb.FileDescriptorProto
	seen := make(map[string]bool)
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			add(imports.Get(i).FileDescriptor)
		}
		files = append(files, protodesc.ToFileDescriptorProto(fd))
	}
	add(compiled[0])
	for _, plugin := range []string{"google.golang.org/protobuf/cmd/protoc-gen-go", "google.golang.org/grpc/cmd/protoc-gen-go-grpc"} {
		request := &pluginpb.CodeGeneratorRequest{
			FileToGenerate: []string{file},
			Parameter:      proto.String("paths=source_relative"),
			ProtoFile:      files,
		}
		if err = invoke(ctx, plugin, request, dir); err != nil {
			return err
		}
	}
	return nil
}

func invoke(ctx context.Context, plugin string, request *pluginpb.CodeGeneratorRequest, dir string) error {
	input, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	// #nosec G204 -- fixed pinned tool names from go.mod
	command := exec.CommandContext(ctx, "go", "tool", plugin)
	command.Stdin = bytes.NewReader(input)
	command.Stdout = &out
	command.Stderr = os.Stderr
	if err = command.Run(); err != nil {
		return fmt.Errorf("running %s: %w", plugin, err)
	}
	var response pluginpb.CodeGeneratorResponse
	if err = proto.Unmarshal(out.Bytes(), &response); err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("%s: %s", plugin, response.GetError())
	}
	for _, generated := range response.File {
		target := filepath.Join(dir, generated.GetName())
		if err = os.WriteFile(target, []byte(generated.GetContent()), 0o644); err != nil { // #nosec G306 -- source file committed to the repository
			return err
		}
	}
	return nil
}
