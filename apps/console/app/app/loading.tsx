"use client";

import { Skeleton } from "@heroui/react";
export default function Loading() {
  return (
    <div aria-busy="true">
      <Skeleton className="mb-4 h-8 w-48 rounded-lg" />
      <Skeleton className="mb-8 h-4 w-72 rounded-lg" />
      <Skeleton className="h-80 rounded-xl" />
    </div>
  );
}
