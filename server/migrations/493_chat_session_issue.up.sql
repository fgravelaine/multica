-- A chat may be ABOUT a unit.
--
-- A chat session already carries an optional project, so an agent talking to
-- you can hold that project's durable context. It could not hold an issue, and
-- that is the gap this closes: deciding a raised hand is a conversation, and
-- the only surface that had the unit was the comment thread — where every
-- message dispatches a WORK run against the issue brief. The agent arrives
-- ready to do the ticket when what you wanted was to argue about the question.
--
-- Nullable, and soft like project_id: no foreign key, so deleting an issue
-- leaves the conversation readable instead of taking it with it. What the chat
-- loses is the context selection, not its history.
ALTER TABLE chat_session
    ADD COLUMN issue_id UUID;

CREATE INDEX idx_chat_session_issue
    ON chat_session (issue_id)
    WHERE issue_id IS NOT NULL;
