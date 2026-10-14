module github.com/studyzy/wenyuan

go 1.24

require (
	github.com/jackc/pgx/v5 v5.5.4
	// robfig/cron parses the mental-model refresh schedules stored in a
	// trigger. It computes the previous fire time only; scheduling stays with
	// the maintenance loop's own cadence. MIT-licensed, pure Go.
	github.com/robfig/cron/v3 v3.0.1
	github.com/tiktoken-go/tokenizer v0.7.0
)

require (
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	golang.org/x/crypto v0.17.0 // indirect
	golang.org/x/sync v0.5.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)
