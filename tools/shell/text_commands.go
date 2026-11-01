package shell

// textCommands are the GNU-compatible text tools: grep, sed, head, tail, wc,
// sort, uniq, cut, tr, diff, jq and awk.
func textCommands() []Command {
	return []Command{headCommand(), tailCommand(), wcCommand(), sortCommand(), uniqCommand(), cutCommand(), trCommand(), grepCommand(), sedCommand(), diffCommand()}
}
