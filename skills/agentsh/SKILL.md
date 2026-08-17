# agentsh tools

agentsh provides the primary `Bash` tool plus durable execution controls. Use the recovery call printed in any non-clean result literally.

## Proactive tools

- `Bash(command, session?, timeout?, background?, stdin?)`: run a command. Use `background: true` for long-running work.
- `BashOutput(id, stream?, lines?, grep?, context?)`: retrieve output without placing the full stream in context. Page large output by line range or search with grep.
- `BashProcesses(session?)`: inspect live invocations.
- `BashKill(id, signal?)`: terminate a process tree.
- `BashInput(id, data)`: answer an interactive process that explicitly reports waiting on input.
- `BashHistory(session?, cmd?, exit?, since?, limit?)`: query prior commands and failures.
- `BashState(session?)`: inspect carried cwd, environment, functions, shell options, and active jobs.
- `BashReplay(id)`: re-run a recorded invocation verbatim.

## Output discipline

Output is stored outside context. Read only the relevant range or grep result. Truncation notices include exact totals and literal `BashOutput` calls for recovery.

Do not use raw shell access to bypass agentsh execution controls. Always use the tool named `Bash` for command execution.
