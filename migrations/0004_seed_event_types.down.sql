SET search_path TO hub, public;

DELETE FROM hub.event_types
WHERE owner_tool_id IS NULL
  AND (name, version) IN (
      ('user.created', 1),
      ('document.generated', 1),
      ('verification.completed', 1)
  );
