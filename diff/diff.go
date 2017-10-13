package diff

import (
	"fmt"

	"github.com/golang/protobuf/protoc-gen-go/descriptor"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/google/go-cmp/cmp"
)

// Changing a protofile Name should be fine. The package name is never determined
// by the filename.
// Backwards incompatible changes:
// - Removing a RPC endpoint
// - Changing the input or output message type
// - Nesting / Unnesting a message or enum type
// - Looking at options is important too

// Things that would require code changes
// - What if they change the java package name?
// - Renaming a field? (if using the JSON output)
// - Renaming an enum field?
// - Marking a field as repeated

// There are two types of changes: ones that will break existing clients, and
// ones that will require new code changes

// This package works by manually checking for differences
// We could instead write a general comparison algorithm that diffs two golang structs
// We chould then use the reflect package to pick which fields to compare
// Generatelized:
//   Operation: Added, Removed, Changed
//   Type: MessageField, Message
//   Ident: String (filename, message name, etc)
type Change interface {
	String() string
}

type Report struct {
	Changes []Change
}

func (r *Report) Add(ch Change) {
	r.Changes = append(r.Changes, ch)
}

func Diff(previous, current *plugin.CodeGeneratorRequest) (*Report, error) {
	return diffFiles(previous.ProtoFile, current.ProtoFile)
}

func DiffSet(previous, current *descriptor.FileDescriptorSet) (*Report, error) {
	return diffFiles(previous.File, current.File)
}

func diffFiles(previous, current []*descriptor.FileDescriptorProto) (*Report, error) {
	report := &Report{Changes: []Change{}}

	prev_by_package := groupByPackage(previous)
	curr_by_package := groupByPackage(current)

	for package_name, files := range prev_by_package {
		next, exists := curr_by_package[package_name]
		if !exists {
			report.Add(ProblemRemovedPackage{package_name})
			continue
		}
		diffPackage(report, files, next)
	}

	var err error
	if len(report.Changes) > 0 {
		err = fmt.Errorf("found %d problems: %s", len(report.Changes), report.Changes)
	}
	return report, err
}

func groupByPackage(files []*descriptor.FileDescriptorProto) map[string][]*descriptor.FileDescriptorProto {
	by_package := map[string][]*descriptor.FileDescriptorProto{}

	for _, file := range files {
		package_files, exists := by_package[*file.Package]
		if exists {
			by_package[*file.Package] = append(package_files, file)
		} else {
			by_package[*file.Package] = []*descriptor.FileDescriptorProto{file}
		}
	}

	return by_package
}

func diffPackage(report *Report, previous, current []*descriptor.FileDescriptorProto) {
	{ // EnumType
		curr := map[string]*descriptor.EnumDescriptorProto{}
		for _, file := range current {
			for _, enum := range file.EnumType {
				curr[*enum.Name] = enum
			}
		}
		for _, file := range previous {
			for _, enum := range file.EnumType {
				next, exists := curr[*enum.Name]
				if !exists {
					report.Add(ProblemRemovedEnum{*enum.Name})
					continue
				}
				diffEnum(report, enum, next)
			}
		}
	}

	{ // Service
		curr := map[string]*descriptor.ServiceDescriptorProto{}
		for _, file := range current {
			for _, srv := range file.Service {
				curr[*srv.Name] = srv
			}
		}
		for _, file := range previous {
			for _, srv := range file.Service {
				next, exists := curr[*srv.Name]
				if !exists {
					report.Add(ProblemRemovedService{*srv.Name})
					continue
				}
				diffService(report, srv, next)
			}
		}
	}

	{ // MessageType
		curr := map[string]*descriptor.DescriptorProto{}
		for _, file := range current {
			for _, msg := range file.MessageType {
				curr[*msg.Name] = msg
			}
		}
		for _, file := range previous {
			for _, msg := range file.MessageType {
				next, exists := curr[*msg.Name]
				if !exists {
					report.Add(ProblemRemovedMessage{*msg.Name})
					continue
				}
				diffMsg(report, msg, next)
			}
		}
	}
}

func diffMsg(report *Report, previous, current *descriptor.DescriptorProto) {
	curr := map[int32]*descriptor.FieldDescriptorProto{}

	for _, field := range current.Field {
		curr[*field.Number] = field
	}

	for _, field := range previous.Field {
		next, exists := curr[*field.Number]
		if !exists {
			report.Add(ProblemRemovedField{*current.Name, *field.Name})
			continue
		}
		if !cmp.Equal(field.Name, next.Name) {
			report.Add(ProblemChangedFieldName{
				Message: *current.Name,
				Number:  *field.Number,
				OldName: field.Name,
				NewName: next.Name,
			})
		}
		if !cmp.Equal(field.Type, next.Type) {
			report.Add(ProblemChangedFieldType{
				Message: *current.Name,
				Field:   *field.Name,
				OldType: field.Type,
				NewType: next.Type,
			})
		}
		if !cmp.Equal(field.Label, next.Label) {
			report.Add(ProblemChangedFieldLabel{
				Message:  *current.Name,
				Field:    *field.Name,
				OldLabel: field.Label,
				NewLabel: next.Label,
			})
		}

	}
}

func diffEnum(report *Report, previous, current *descriptor.EnumDescriptorProto) {
	byvalue := map[int32]*descriptor.EnumValueDescriptorProto{}
	byname := map[string]*descriptor.EnumValueDescriptorProto{}

	for _, value := range current.Value {
		byvalue[*value.Number] = value
	}

	for _, value := range current.Value {
		byname[*value.Name] = value
	}

	for _, value := range previous.Value {
		_, exists := byvalue[*value.Number]
		if !exists {
			next, renamed := byname[*value.Name]
			if renamed {
				report.Add(ProblemChangeEnumValue{
					Enum:     *previous.Name,
					Name:     *value.Name,
					OldValue: *value.Number,
					NewValue: *next.Number,
				})
			} else {
				report.Add(ProblemRemovedEnumValue{*previous.Name, *value.Name})
			}
		}
	}
}

// Golang go-cmp
func diffService(report *Report, previous, current *descriptor.ServiceDescriptorProto) {
	curr := map[string]*descriptor.MethodDescriptorProto{}

	for _, value := range current.GetMethod() {
		curr[*value.Name] = value
	}

	for _, prev := range previous.GetMethod() {
		next, exists := curr[*prev.Name]
		if !exists {
			report.Add(ProblemRemovedServiceMethod{*previous.Name, *prev.Name})
			continue
		}
		if !cmp.Equal(next.InputType, prev.InputType) {
			report.Add(ProblemChangedService{
				Service: *current.Name,
				Side:    "input",
				Name:    *prev.Name,
				OldType: *prev.InputType,
				NewType: *next.InputType,
			})
		}
		if !cmp.Equal(next.OutputType, prev.OutputType) {
			report.Add(ProblemChangedService{
				Service: *current.Name,
				Side:    "output",
				Name:    *prev.Name,
				OldType: *prev.OutputType,
				NewType: *next.OutputType,
			})
		}
		if !cmp.Equal(prev.ClientStreaming, next.ClientStreaming) {
			report.Add(ProblemChangedServiceStreaming{
				Service:   *current.Name,
				Name:      *prev.Name,
				Side:      "client",
				OldStream: prev.ClientStreaming,
				NewStream: next.ClientStreaming,
			})
		}
		if !cmp.Equal(prev.ServerStreaming, next.ServerStreaming) {
			report.Add(ProblemChangedServiceStreaming{
				Service:   *current.Name,
				Name:      *prev.Name,
				Side:      "server",
				OldStream: prev.ServerStreaming,
				NewStream: next.ServerStreaming,
			})
		}

	}
}
