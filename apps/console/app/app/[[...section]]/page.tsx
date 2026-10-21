import { ConsolePage } from "@/components/console-pages";
export default async function Page({
  params,
}: {
  params: Promise<{ section?: string[] }>;
}) {
  const { section = [] } = await params;
  return <ConsolePage section={section.join("/")} />;
}
