// Generates the single-message descriptor without requiring a system protoc.
package main

import (
	"bytes"
	"fmt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
	"os"
	"os/exec"
)

func main() {
	f := &descriptorpb.FileDescriptorProto{Name: proto.String("core/dualtls/config.proto"), Package: proto.String("v2nb.dualtls"), Syntax: proto.String("proto3"), Options: &descriptorpb.FileOptions{GoPackage: proto.String("github.com/wyx2685/v2node/core/dualtls")}, MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Config"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("anytls_settings"), JsonName: proto.String("anytlsSettings"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum()}}}}}
	b, err := proto.Marshal(&pluginpb.CodeGeneratorRequest{ProtoFile: []*descriptorpb.FileDescriptorProto{f}, FileToGenerate: []string{f.GetName()}, Parameter: proto.String("paths=source_relative")})
	must(err)
	cmd := exec.Command("go", "run", "google.golang.org/protobuf/cmd/protoc-gen-go")
	cmd.Stdin = bytes.NewReader(b)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	must(err)
	response := new(pluginpb.CodeGeneratorResponse)
	must(proto.Unmarshal(out, response))
	if response.GetError() != "" {
		panic(response.GetError())
	}
	for _, file := range response.File {
		must(os.WriteFile(file.GetName(), []byte(file.GetContent()), 0644))
		fmt.Println(file.GetName())
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
