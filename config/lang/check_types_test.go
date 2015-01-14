package lang

import (
	"testing"

	"github.com/hashicorp/terraform/config/lang/ast"
)

func TestTypeCheck(t *testing.T) {
	cases := []struct {
		Input string
		Scope *Scope
		Error bool
	}{
		{
			"foo",
			&Scope{},
			false,
		},

		{
			"foo ${bar}",
			&Scope{
				VarMap: map[string]Variable{
					"bar": Variable{
						Value: "baz",
						Type:  ast.TypeString,
					},
				},
			},
			false,
		},

		{
			"foo ${rand()}",
			&Scope{
				FuncMap: map[string]Function{
					"rand": Function{
						ReturnType: ast.TypeString,
						Callback: func([]interface{}) (interface{}, error) {
							return "42", nil
						},
					},
				},
			},
			false,
		},

		{
			`foo ${rand("42")}`,
			&Scope{
				FuncMap: map[string]Function{
					"rand": Function{
						ArgTypes:   []ast.Type{ast.TypeString},
						ReturnType: ast.TypeString,
						Callback: func([]interface{}) (interface{}, error) {
							return "42", nil
						},
					},
				},
			},
			false,
		},

		{
			`foo ${rand(42)}`,
			&Scope{
				FuncMap: map[string]Function{
					"rand": Function{
						ArgTypes:   []ast.Type{ast.TypeString},
						ReturnType: ast.TypeString,
						Callback: func([]interface{}) (interface{}, error) {
							return "42", nil
						},
					},
				},
			},
			true,
		},

		{
			`foo ${rand()}`,
			&Scope{
				FuncMap: map[string]Function{
					"rand": Function{
						ArgTypes:     nil,
						ReturnType:   ast.TypeString,
						Variadic:     true,
						VariadicType: ast.TypeString,
						Callback: func([]interface{}) (interface{}, error) {
							return "42", nil
						},
					},
				},
			},
			false,
		},

		{
			`foo ${rand("42")}`,
			&Scope{
				FuncMap: map[string]Function{
					"rand": Function{
						ArgTypes:     nil,
						ReturnType:   ast.TypeString,
						Variadic:     true,
						VariadicType: ast.TypeString,
						Callback: func([]interface{}) (interface{}, error) {
							return "42", nil
						},
					},
				},
			},
			false,
		},

		{
			`foo ${rand("42", 42)}`,
			&Scope{
				FuncMap: map[string]Function{
					"rand": Function{
						ArgTypes:     nil,
						ReturnType:   ast.TypeString,
						Variadic:     true,
						VariadicType: ast.TypeString,
						Callback: func([]interface{}) (interface{}, error) {
							return "42", nil
						},
					},
				},
			},
			true,
		},

		{
			"foo ${bar}",
			&Scope{
				VarMap: map[string]Variable{
					"bar": Variable{
						Value: 42,
						Type:  ast.TypeInt,
					},
				},
			},
			true,
		},

		{
			"foo ${rand()}",
			&Scope{
				FuncMap: map[string]Function{
					"rand": Function{
						ReturnType: ast.TypeInt,
						Callback: func([]interface{}) (interface{}, error) {
							return 42, nil
						},
					},
				},
			},
			true,
		},
	}

	for _, tc := range cases {
		node, err := Parse(tc.Input)
		if err != nil {
			t.Fatalf("Error: %s\n\nInput: %s", err, tc.Input)
		}

		visitor := &TypeCheck{Scope: tc.Scope}
		err = visitor.Visit(node)
		if (err != nil) != tc.Error {
			t.Fatalf("Error: %s\n\nInput: %s", err, tc.Input)
		}
	}
}

func TestTypeCheck_implicit(t *testing.T) {
	implicitMap := map[ast.Type]map[ast.Type]string{
		ast.TypeInt: {
			ast.TypeString: "intToString",
		},
	}

	cases := []struct {
		Input string
		Scope *Scope
		Error bool
	}{
		{
			"foo ${bar}",
			&Scope{
				VarMap: map[string]Variable{
					"bar": Variable{
						Value: 42,
						Type:  ast.TypeInt,
					},
				},
			},
			false,
		},

		{
			"foo ${foo(42)}",
			&Scope{
				FuncMap: map[string]Function{
					"foo": Function{
						ArgTypes:   []ast.Type{ast.TypeString},
						ReturnType: ast.TypeString,
					},
				},
			},
			false,
		},

		{
			`foo ${foo("42", 42)}`,
			&Scope{
				FuncMap: map[string]Function{
					"foo": Function{
						ArgTypes:     []ast.Type{ast.TypeString},
						Variadic:     true,
						VariadicType: ast.TypeString,
						ReturnType:   ast.TypeString,
					},
				},
			},
			false,
		},
	}

	for _, tc := range cases {
		node, err := Parse(tc.Input)
		if err != nil {
			t.Fatalf("Error: %s\n\nInput: %s", err, tc.Input)
		}

		// Modify the scope to add our conversion functions.
		if tc.Scope.FuncMap == nil {
			tc.Scope.FuncMap = make(map[string]Function)
		}
		tc.Scope.FuncMap["intToString"] = Function{
			ArgTypes:   []ast.Type{ast.TypeInt},
			ReturnType: ast.TypeString,
		}

		// Do the first pass...
		visitor := &TypeCheck{Scope: tc.Scope, Implicit: implicitMap}
		err = visitor.Visit(node)
		if (err != nil) != tc.Error {
			t.Fatalf("Error: %s\n\nInput: %s", err, tc.Input)
		}
		if err != nil {
			continue
		}

		// If we didn't error, then the next type check should not fail
		// WITHOUT implicits.
		visitor = &TypeCheck{Scope: tc.Scope}
		err = visitor.Visit(node)
		if err != nil {
			t.Fatalf("Error: %s\n\nInput: %s", err, tc.Input)
		}
	}
}
