package netinspect

import "testing"

func TestCalicoSelectorGrammar(t *testing.T) {
	set := map[string]string{"app": "api", "tier": "backend", "env": "prod", "projectcalico.org/namespace": "shop"}
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"all()", true},
		{"global()", false},
		{"has(app)", true},
		{"has(missing)", false},
		{"!has(missing)", true},
		{"! has(app)", false},
		{`app == "api"`, true},
		{`app == 'api'`, true},
		{`app == "web"`, false},
		{`missing == "x"`, false},
		{`app != "web"`, true},
		{`app != "api"`, false},
		{`missing != "x"`, true}, // an absent label satisfies !=
		{`app in {"api", "web"}`, true},
		{`app in {'web'}`, false},
		{`app in {}`, false},
		{`missing in {"x"}`, false},
		{`app not in {"web", "db"}`, true},
		{`app not in {"api"}`, false},
		{`missing not in {"x"}`, true}, // and so does not in
		{`tier contains "ack"`, true},
		{`tier contains "zzz"`, false},
		{`missing contains "a"`, false},
		{`tier starts with "back"`, true},
		{`tier starts with "end"`, false},
		{`tier ends with "end"`, true},
		{`tier ends with "back"`, false},
		{`projectcalico.org/namespace == "shop"`, true},
		{`app == "api" && env == "prod"`, true},
		{`app == "api" && env == "dev"`, false},
		{`app == "web" || env == "prod"`, true},
		{`app == "web" || env == "dev"`, false},
		// && binds tighter than ||: false || (true && true)
		{`app == "web" || app == "api" && env == "prod"`, true},
		// true || (false && false) would be true; (true || false) && false is not
		{`app == "api" || app == "web" && env == "dev"`, true},
		{`(app == "api" || app == "web") && env == "dev"`, false},
		// ! binds tighter than &&
		{`!has(missing) && app == "api"`, true},
		{`!(app == "api" && env == "prod")`, false},
		{`!!has(app)`, true},
		{`has(app) && !has(missing) && (env in {"prod"} || tier == "x")`, true},
	} {
		sel, err := parseCalicoSelector(tc.text)
		if err != nil {
			t.Errorf("%q: %v", tc.text, err)
			continue
		}
		if got := matchSet(sel, set); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestACalicoSelectorItCannotReadIsAnError(t *testing.T) {
	for _, text := range []string{
		`app ==`, `app == api`, `app = "api"`, `app in "a"`, `app in {"a" "b"}`, `app in {"a"`, `app not "a"`,
		`app starts "a"`, `app ends "a"`, `has()`, `has(a`, `all`, `all(`, `(app == "a"`, `app == "a")`, `app == "a" &&`,
		`&& app == "a"`, `app == "a" app == "b"`, `app`, `app == "unclosed`, `app == "a" ; x`, `global`, `!`,
	} {
		if _, err := parseCalicoSelector(text); err == nil {
			t.Errorf("%q should not parse", text)
		}
	}
}
