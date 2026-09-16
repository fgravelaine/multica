"use client";

import { MissionList } from "@multica/views/missions/components";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function MissionsPage() {
  return (
    <ErrorBoundary>
      <MissionList />
    </ErrorBoundary>
  );
}
