"use client";

// SPIKE (not upstream): the mission view route.
//
// `id` accepts a UUID or a human identifier such as SPIK-7 — the server's
// loadIssueForUser resolves both, so a pasted issue key works here too.
import { use } from "react";
import { MissionView } from "@multica/views/missions/components";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function MissionPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return (
    <ErrorBoundary resetKeys={[id]}>
      <MissionView issueId={id} />
    </ErrorBoundary>
  );
}
