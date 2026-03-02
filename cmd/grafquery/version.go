package grafquery

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "0.2.4"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("grafquery", version)
		},
	}
}
