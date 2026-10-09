"""Live end-to-end check: starts the server over stdio and calls real tools."""

import asyncio
import os
import sys
from pathlib import Path

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

HERE = Path(__file__).parent


async def main() -> None:
    params = StdioServerParameters(
        command="uv",
        args=["run", "--directory", str(HERE), "python", "server.py"],
        env=dict(os.environ),  # the SDK only forwards a safe allowlist by default
    )
    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            tools = await session.list_tools()
            print("tools:", [t.name for t in tools.tools])

            stats = await session.call_tool("trakt_stats", {})
            print("\ntrakt_stats:", stats.content[0].text)

            watched = await session.call_tool("trakt_watched_movies", {"limit": 3})
            print("\ntrakt_watched_movies(3):", watched.content[0].text)

            shows = await session.call_tool("trakt_watched_shows", {"limit": 3})
            print("\ntrakt_watched_shows(3):", shows.content[0].text)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        sys.exit(1)
