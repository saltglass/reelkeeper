import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

/** A pointer to the one-off Radarr import, which isn't in the main menu. */
export function ImportRadarrCard() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Moving from Radarr?</CardTitle>
        <CardDescription>
          Bring over what only Radarr knows: wanted movies, monitoring, tags, history and the
          blocklist. Your files are found by scanning the folders above.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button asChild variant="outline">
          <Link to="/settings/import-radarr">Import from Radarr</Link>
        </Button>
      </CardContent>
    </Card>
  );
}
