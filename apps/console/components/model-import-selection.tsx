"use client";

import { useState } from "react";
import { Button, Checkbox, CheckboxGroup, Chip, Input } from "@heroui/react";
import { Search } from "lucide-react";
import { en as t } from "@/i18n/en";
import { pricingEn as p } from "@/i18n/pricing-en";
import { count } from "@/lib/api";

export interface ModelImportOption {
  id: string;
  name: string;
  unavailable?: "configured" | "restricted" | "unsupported" | "alias_required";
}

export function ModelImportSelection({
  models,
  value,
  onValueChange,
  isDisabled = false,
}: {
  models: ModelImportOption[];
  value: string[];
  onValueChange: (ids: string[]) => void;
  isDisabled?: boolean;
}) {
  const [search, setSearch] = useState("");
  const availableIDs = new Set(
    models.filter((model) => !model.unavailable).map((model) => model.id),
  );
  const selected = new Set(value.filter((id) => availableIDs.has(id)));
  const visible = models.filter((model) =>
    `${model.id} ${model.name}`.toLowerCase().includes(search.trim().toLowerCase()),
  );
  const visibleIDs = new Set(
    visible.filter((model) => !model.unavailable).map((model) => model.id),
  );
  const selectedVisible = [...visibleIDs].filter((id) => selected.has(id));
  const allVisibleSelected =
    visibleIDs.size > 0 && selectedVisible.length === visibleIDs.size;
  const unavailableLabels = {
    configured: t.modelImportExists,
    restricted: t.modelImportRestricted,
    unsupported: t.modelImportUnsupported,
    alias_required: t.modelImportAliasRequired,
  };

  function changeVisibleSelection(next: string[]) {
    if (isDisabled) return;
    const retained = [...selected].filter((id) => !visibleIDs.has(id));
    onValueChange([
      ...retained,
      ...next.filter((id) => visibleIDs.has(id)),
    ]);
  }

  return (
    <div className="rp-management-selection">
      <Input
        aria-label={t.modelImportSearch}
        placeholder={t.modelImportSearch}
        value={search}
        onValueChange={setSearch}
        isDisabled={isDisabled}
        variant="bordered"
        isClearable
        onClear={() => setSearch("")}
        startContent={<Search size={16} />}
      />
      <div className="rp-management-selection-tools">
        <Checkbox
          isSelected={allVisibleSelected}
          isIndeterminate={selectedVisible.length > 0 && !allVisibleSelected}
          isDisabled={isDisabled || !visibleIDs.size}
          onValueChange={(checked) =>
            changeVisibleSelection(checked ? [...visibleIDs] : [])
          }
          classNames={{ base: "m-0 min-h-11 p-0", label: "text-sm" }}
        >
          {(search.trim()
            ? t.modelImportSelectMatching
            : t.modelImportSelectAll
          ).replace("{count}", count(visibleIDs.size))}
        </Checkbox>
        <Button
          size="sm"
          variant="light"
          isDisabled={isDisabled || !selected.size}
          onPress={() => onValueChange([])}
          className="min-h-11"
        >
          {t.modelImportClearSelection}
        </Button>
      </div>
      {visible.length ? (
        <CheckboxGroup
          aria-label={t.modelImportChooseModels}
          value={[...selected]}
          onValueChange={changeVisibleSelection}
          isDisabled={isDisabled}
          classNames={{
            wrapper: "rp-management-model-options",
          }}
        >
          {visible.map((model) => (
            <Checkbox
              key={model.id}
              value={model.id}
              isDisabled={isDisabled || !!model.unavailable}
              classNames={{
                base: "rp-management-model-option",
                wrapper: "mt-0.5 shrink-0",
                label: "min-w-0 flex-1",
              }}
            >
              <span className="flex flex-wrap items-center gap-2 text-sm">
                <span className="min-w-0 break-all">
                  {model.name || model.id}
                </span>
                {model.unavailable && (
                  <Chip radius="sm" size="sm" variant="flat">
                    {unavailableLabels[model.unavailable]}
                  </Chip>
                )}
              </span>
              <span className="mt-1 block break-all text-xs leading-5 text-default-500">
                {model.id}
              </span>
            </Checkbox>
          ))}
        </CheckboxGroup>
      ) : (
        <p className="py-5 text-sm text-default-500">
          {search.trim() ? t.noResults : t.modelImportEmpty}
        </p>
      )}
      <p role="status" aria-live="polite" className="rp-management-selection-status">
        <strong>{t.modelImportSelectedCount.replace("{count}", count(selected.size))}</strong>
        {!search.trim() && <span>{p.selectedHelp}</span>}
        {search.trim() && selected.size > selectedVisible.length && (
          <span className="ml-1">{t.modelImportSelectionRetained}</span>
        )}
      </p>
    </div>
  );
}
