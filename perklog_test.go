package main

import "testing"

func TestParsePerkLogLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *perkEvent // nil means "must fail to parse"
	}{
		{
			name: "login, real sample from a live server",
			line: `[06-08-26 08:34:59.194] [76561197965988309][Edd1e360][6764,5380,0][Login][Hours Survived: 472].`,
			want: &perkEvent{SteamID: "76561197965988309", Username: "Edd1e360", X: 6764, Y: 5380, Z: 0, Kind: "login", HoursSurvived: 472},
		},
		{
			name: "skill dump, real sample from a live server",
			line: `[06-08-26 08:34:59.195] [76561197965988309][Edd1e360][6764,5380,0][Cooking=0, Fitness=5, Strength=6][Hours Survived: 472].`,
			want: &perkEvent{SteamID: "76561197965988309", Username: "Edd1e360", X: 6764, Y: 5380, Z: 0, Kind: "skills",
				Skills: map[string]int{"Cooking": 0, "Fitness": 5, "Strength": 6}, HoursSurvived: 472},
		},
		{
			name: "died",
			line: `[06-08-26 10:00:00.000] [76561197965988309][Edd1e360][100,200,0][Died][Hours Survived: 600.5].`,
			want: &perkEvent{SteamID: "76561197965988309", Username: "Edd1e360", X: 100, Y: 200, Z: 0, Kind: "died", HoursSurvived: 600.5},
		},
		{
			name: "level changed",
			line: `[06-08-26 18:20:13.658] [76561197965988309][Edd1e360][10094,8263,1][Level Changed][Woodwork][4][Hours Survived: 620].`,
			want: &perkEvent{SteamID: "76561197965988309", Username: "Edd1e360", X: 10094, Y: 8263, Z: 1, Kind: "level_changed",
				SkillName: "Woodwork", SkillLevel: 4, HoursSurvived: 620},
		},
		{
			name: "level changed with extra whitespace between brackets",
			line: `[06-08-26 18:20:13.658] [76561197965988309] [Edd1e360] [10094,8263,1] [Level Changed][Woodwork][4] [Hours Survived: 620].`,
			want: &perkEvent{SteamID: "76561197965988309", Username: "Edd1e360", X: 10094, Y: 8263, Z: 1, Kind: "level_changed",
				SkillName: "Woodwork", SkillLevel: 4, HoursSurvived: 620},
		},
		{
			name: "created player",
			line: `[06-08-26 07:00:00.000] [76561197965988309][Edd1e360][0,0,0][Created Player][Hours Survived: 0].`,
			want: &perkEvent{SteamID: "76561197965988309", Username: "Edd1e360", X: 0, Y: 0, Z: 0, Kind: "created_player", HoursSurvived: 0},
		},
		{
			name: "garbage line does not parse",
			line: `this is not a perklog line at all`,
			want: nil,
		},
		{
			name: "unrecognized event keyword is ignored, not guessed at",
			line: `[06-08-26 07:00:00.000] [76561197965988309][Edd1e360][0,0,0][SomeFutureEventType][Hours Survived: 0].`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePerkLogLine(tc.line)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected parse failure, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %+v, got nil (parse failed)", tc.want)
			}
			if got.SteamID != tc.want.SteamID || got.Username != tc.want.Username ||
				got.X != tc.want.X || got.Y != tc.want.Y || got.Z != tc.want.Z ||
				got.Kind != tc.want.Kind || got.HoursSurvived != tc.want.HoursSurvived ||
				got.SkillName != tc.want.SkillName || got.SkillLevel != tc.want.SkillLevel {
				t.Fatalf("mismatch:\n got:  %+v\n want: %+v", got, tc.want)
			}
			if tc.want.Skills != nil {
				if len(got.Skills) != len(tc.want.Skills) {
					t.Fatalf("skills length mismatch: got %v want %v", got.Skills, tc.want.Skills)
				}
				for k, v := range tc.want.Skills {
					if got.Skills[k] != v {
						t.Fatalf("skill %s: got %d want %d", k, got.Skills[k], v)
					}
				}
			}
		})
	}
}
