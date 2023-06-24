# Code harvest
This project was created with the purpose of incorporating a dashboard on my
[website][1] with data derived from my coding sessions:

![Screenshot of website][2]

![Screenshot of website][3]

## Overview
The project has grown over time, and as of now it consists of six separate
components:

- Server for handling coding sessions via RPC
- A client designed for transmitting remote procedure calls to the server
- CLI to aggregate the data over various time spans
- Neovim plugin that maps autocommands to remote procedure calls
- API designed to distribute the data
- A website for presenting the data

The server, client, and CLI is part of this repository. The neovim plugin can
be found [here][4].

### Server
The server operates in the background as a daemon. It handles remote procedure
calls from neovim pertaining to events such as the opening of buffers,
windows gaining focus, the initiation of new neovim instances, etc.

For each instance of neovim, I establish a new coding session. This leads to
the creation of several sessions per day. Every session is stored temporarily
on the file system. The sessions are subsequently clustered, according to the
day of occurrence, and merged before they are written to a permanent storage
location. This is primarily to avoid surpassing any limits set by free database
tiers.

### Client
The client uses the neovim [go-client][5] to add commands to neovim which I have
mapped to autocommands in the neovim [plugin][4].

### CLI for aggregation
I use the CLI to aggregate raw coding sessions by day, and daily coding sessions by
week, month, and year.

[1]: https://creativecreature.com
[2]: ./screenshots/website1.png
[3]: ./screenshots/website2.png
[4]: https://github.com/creativecreature/vim-code-harvest
[5]: https://github.com/neovim/go-client
