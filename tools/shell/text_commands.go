package shell

// textCommands are the GNU-compatible text tools: grep, sed, head, tail, wc,
// sort, uniq, cut, tr, diff, jq and awk.
func textCommands() []Command {
	return []Command{
		grepCommand(), sedCommand(), headCommand(), tailCommand(), wcCommand(), sortCommand(),
		uniqCommand(), cutCommand(), trCommand(), diffCommand(), jqCommand(), awkCommand(),
	}
}
