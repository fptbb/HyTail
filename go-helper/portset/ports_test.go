package portset

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	x, err := Parse("25566, 25567-25568")
	if err != nil || !reflect.DeepEqual(x, []int{25566, 25567, 25568}) {
		t.Fatalf("%v %v", x, err)
	}
	for _, s := range []string{"", "0", "1-200", "65536", "9-8", "7,7", "99,", "-1", "1--2"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
func TestMapping(t *testing.T) {
	remote := []int{19132, 19133, 19135}
	v, err := Mapping(remote, []int{20000})
	if err != nil || !reflect.DeepEqual(v, []int{20000, 20001, 20003}) {
		t.Fatalf("%v %v", v, err)
	}
	if _, err := Mapping(remote, []int{65535}); err == nil {
		t.Fatal("accepted overflowing mapping")
	}
}
