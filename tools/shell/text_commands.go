package shell

// textCommands are the GNU-compatible text tools: grep, sed, head, tail, wc,
// sort, uniq, cut, tr, diff, jq and awk. Each fails with status 2 on an
// input beyond maximumInputBytes.
func textCommands() []Command {
	commands := []Command{
		grepCommand(), sedCommand(), headCommand(), tailCommand(), wcCommand(), sortCommand(),
		uniqCommand(), cutCommand(), trCommand(), diffCommand(), jqCommand(), awkCommand(),
	}
	for index, command := range commands {
		commands[index] = boundedInput(command)
	}
	return commands
}
