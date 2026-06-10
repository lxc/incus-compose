package main

func (s *E2ESuite) TestLifecycleHealthd() {
	compose := "../../test/fixtures/healthd-debug/compose.yaml"

	defer func() {
		_, _, _ = s.run("-f", compose, "down", "--project")
	}()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list",
			args: []string{"-f", compose, "list"},
		},
		{
			name: "healthd logs",
			args: []string{"-f", compose, "healthd", "logs"},
		},
		{
			name: "healthd reload",
			args: []string{"-f", compose, "healthd", "reload"},
		},
		{
			name: "healthd restart",
			args: []string{"-f", compose, "healthd", "restart"},
		},
		{
			name: "healthd down",
			args: []string{"-f", compose, "healthd", "down"},
		},
		{
			name: "healthd up --recreate",
			args: []string{"-f", compose, "healthd", "up", "--recreate"},
		},
		{
			name: "down",
			args: []string{"-f", compose, "down", "--project"},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			_, _, err := s.run(tt.args...)
			s.Require().NoError(err)
		})
	}
}
