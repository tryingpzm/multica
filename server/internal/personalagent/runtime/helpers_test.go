package runtime_test

import "github.com/multica-ai/multica/server/internal/personalagent/executor"

func executorReport(kind, summary string, question *string) executor.ReportArgs {
	return executor.ReportArgs{Kind: kind, Summary: summary, HumanAction: question}
}
