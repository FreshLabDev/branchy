# Telegram Integration

## Commands

`/start` is the only command. It opens the main menu in DM.

All setup happens through inline keyboards. Other text messages are ignored so
the bot remains quiet and predictable.

## Groups

Subscription settings are managed in DM. Group delivery works after:

1. The bot is added to a group or supergroup.
2. Branchy receives a `my_chat_member` update and records the group.
3. The user selects that group in DM.
4. Branchy verifies the user is a group `creator` or `administrator` with
   `getChatMember`.

If Telegram cannot verify admin status, Branchy does not save the group
destination.

## Notifications

Notifications use Telegram HTML parse mode. User-controlled GitHub fields are
escaped before sending.

Message content stays concise and includes:

- repository
- event type
- actor
- branch
- title or summary
- GitHub link

