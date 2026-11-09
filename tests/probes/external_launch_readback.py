"""Authenticated readback for the opt-in Go probe; never sends Telegram messages."""
import asyncio
import os
import sys

sys.path.insert(0, os.environ["TELEGRAM_CLIENT_MODULE_DIR"])
import telegram_client

async def main():
    group, card_id, topic_id = map(int, sys.argv[1:])
    async with telegram_client.connected_client() as client:
        entity = await telegram_client.resolve_chat(client, str(group))
        card = await client.get_messages(entity, ids=card_id)
        assert "Completed" in card.raw_text and "Telegram: connected" in card.raw_text
        assert not any(button.text == "Retry" for row in (card.buttons or []) for button in row)
        messages = await client.get_messages(entity, reply_to=topic_id, limit=12)
        assert any("[Final]" in m.raw_text and "EXTERNAL_LAUNCH_PROBE_OK" in m.raw_text for m in messages)
        print("Launch card and recovered Final verified through authenticated Telegram history.")

asyncio.run(asyncio.wait_for(main(), 50))
