package plan

import "testing"

func TestActivationIdentitySeparatesComponents(t *testing.T) {
	type address struct {
		scope []string
		node  string
	}
	for _, pair := range [][2]address{
		{{[]string{"child:body", "invoke:first"}, "work:step"}, {[]string{"child:body", "invoke:first:work"}, "step"}},
		{{nil, "child:body:invoke:first:work"}, {[]string{"child:body", "invoke:first"}, "work"}},
		{{[]string{"child:a", "invoke:b"}, "work"}, {[]string{"child:a:invoke:b"}, "work"}},
		{{nil, "work:step"}, {nil, "work%3Astep"}},
		{{[]string{"child:a:b"}, "work"}, {[]string{"child:a%3Ab"}, "work"}},
	} {
		left := activationID("run", pair[0].scope, pair[0].node)
		right := activationID("run", pair[1].scope, pair[1].node)
		if left == right {
			t.Errorf("distinct activation addresses share ID %q: %+v", left, pair)
		}
	}
}
